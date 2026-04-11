# Review: Messaging, Outbox & Saga (Silent Failure Hunt)

**Branch:** `review/messaging-saga` **Agent:** `silent-failure-hunter` **Date:** 2026-04-11
**Scope:** `internal/infrastructure/messaging/**`, `internal/application/outbox_relay.go`, `internal/application/saga_compensator.go`

## Summary

Silent failures concentrate in two zones: (1) the SagaConsumer's in-memory retry map, which silently drops poison messages to the floor with no DLQ and only a WARN log, and (2) the payment service's silent `return nil` on invalid input that suppresses what are arguably unrecoverable business errors. A secondary theme is partial compensation ordering in `saga_compensator.go`: a Redis-only revert failure after a successful DB rollback is returned as an error, causing the SagaConsumer to retry the full saga, re-executing the DB path on already-compensated orders (idempotency guard exists, but the retry storm is real). No offset commits are checked for errors in a way that surfaces commit failures; both consumers log-and-forget commit errors, which can cause re-delivery storms silently.

---

## Findings (severity-ranked)

### [CRITICAL] SagaConsumer drops poison messages silently — no DLQ

**File:** [internal/infrastructure/messaging/saga_consumer.go](../../internal/infrastructure/messaging/saga_consumer.go) (lines 63–72)

**Issue:** When `compensator.HandleOrderFailed` fails more than `maxRetries` times, the message is committed and discarded with only a `Warnw` log. The `retries` map is keyed on `msg.Offset` (an `int64`), not on a stable message identity, so if the consumer restarts between retries the counter resets and the effective retry count can be far higher than 3 before a poison message is finally skipped.

**Impact:** A persistently failing compensation event is silently consumed. Inventory may never be reverted. There is no dead-letter record, no alert metric, and no operator recovery path.

**Fix:**
1. Write skipped messages to a DLQ topic (`order.failed.dlq`) via the publisher before committing.
2. Key the retry map on a stable identity (`msg.Partition`+`msg.Offset` string, or a business key extracted from the payload before processing).
3. Emit a `saga_compensation_dlq_total` counter metric when a message is dead-lettered.

---

### [CRITICAL] `payment/service.go` silently drops invalid messages — `return nil` masks business errors

**File:** [internal/application/payment/service.go](../../internal/application/payment/service.go) (lines 43–47)

**Issue:** `ProcessOrder` returns `nil` (not an error) for `OrderID <= 0` and `Amount < 0`. The Kafka consumer interprets `nil` as success and commits the offset. These are unprocessable messages that should be dead-lettered, not silently consumed.

**Impact:** Corrupted or malformed events from upstream are permanently lost. Downstream monitoring never sees them. A publisher bug that emits zero-value `OrderID` would silently drain the topic.

**Fix:** Return a typed sentinel error (e.g., `domain.ErrUnprocessableEvent`) and let the caller decide whether to DLQ or drop. The `KafkaConsumer` should treat unprocessable errors as DLQ candidates, not silently committed messages.

---

### [HIGH] `KafkaConsumer` commit error is log-and-forget — offset advances are not guaranteed

**File:** [internal/infrastructure/messaging/kafka_consumer.go](../../internal/infrastructure/messaging/kafka_consumer.go) (lines 71–73)

**Issue:** `CommitMessages` failure is logged but the loop continues. The offset is not retried. On a transient broker failure, the offset never advances and the same message will be re-delivered after restart — but the caller has no awareness that the offset commit failed, so it cannot distinguish "will retry on restart" from "successfully processed."

**Impact:** Silent at-most-once delivery risk on commit failures. Downstream idempotency guards protect against double-processing on re-delivery, but the lack of retry or back-pressure means the consumer continues fetching new messages while a stale offset is outstanding.

**Fix:** On commit error, either retry the commit with bounded backoff or stop processing new messages until the commit succeeds (return the error to trigger a consumer restart via the caller's error handling).

The same pattern exists in `SagaConsumer` at line 78–80 of [internal/infrastructure/messaging/saga_consumer.go](../../internal/infrastructure/messaging/saga_consumer.go).

---

### [HIGH] Saga partial compensation: Redis revert failure returns error, causing DB re-entry on retry

**File:** [internal/application/saga_compensator.go](../../internal/application/saga_compensator.go) (lines 79–85)

**Issue:** The compensation flow is: (1) DB rollback inside UoW, (2) Redis revert. If step 2 fails and returns an error, the SagaConsumer retries the entire `HandleOrderFailed` call. On retry, step 1 detects `OrderStatusCompensated` and skips via the idempotency guard — correct. But step 2 is retried unconditionally without any idempotency guard of its own at the `HandleOrderFailed` level (the Lua SETNX is the guard, but its key is `order:<id>`, and if the first Redis call partially succeeded and the SETNX key was set, the retry will see the key as already set and silently skip the actual quantity revert).

**Impact:** Whether inventory is actually reverted in Redis depends on the atomicity of the Lua script. If the Lua script sets the SETNX key before reverting inventory (e.g., crashes mid-script), the compensation is permanently skipped on all retries. The log only shows "rollback successful" on the DB path; the Redis path outcome is invisible to the operator on retries.

**Fix:** Add explicit logging of the Redis revert outcome on each retry attempt, including whether it was a no-op due to the SETNX key. Consider a separate idempotency record (DB table) that tracks whether Redis compensation was confirmed, to enable operator alerting when it cannot be verified.

---

### [HIGH] `outbox_relay.go`: advisory lock silently not released on non-graceful shutdown

**File:** [internal/application/outbox_relay.go](../../internal/application/outbox_relay.go) (lines 51–53)

**Issue:** The `_ = r.mutex.Unlock(...)` call on context cancellation discards the unlock error. More critically, the unlock only runs on the `ctx.Done()` path. If the relay goroutine panics mid-batch, or if `Run()` exits abnormally (e.g., the goroutine is killed by the runtime), the advisory lock is never released. PostgreSQL will release it on connection close, but the Fx lifecycle `OnStop` hook does not guarantee connection close before another instance attempts `TryLock`.

**Impact:** In a multi-instance deploy, a crashed leader can block standby instances from acquiring the lock for the duration of the TCP keepalive timeout, causing outbox message delays.

**Fix:** Wrap `processBatch` call in a `defer` that releases the lock, or use `pg_advisory_xact_lock` scoped to a transaction. Log the error from `Unlock`.

---

### [HIGH] `outbox_relay_tracing.go`: span never records errors from `processBatch`

**File:** [internal/application/outbox_relay_tracing.go](../../internal/application/outbox_relay_tracing.go) (lines 26–32)

**Issue:** `processBatchTraced` starts a span and calls `d.next.processBatch(ctx)`, but `processBatch` returns nothing (void). Errors inside `processBatch` are logged but never surfaced to the span. The span always ends with `OK` status even when all publishes fail.

**Impact:** Distributed traces show the outbox relay as healthy even when it is failing every batch. Alerting based on trace error rates will never fire for outbox failures.

**Fix:** Refactor `processBatch` to return an error (or a batch result struct with error count). Record errors on the span via `span.RecordError(err)` and set `span.SetStatus(codes.Error, ...)`.

---

### [MEDIUM] `KafkaConsumer` hardcodes group ID and topic — config bypass

**File:** [internal/infrastructure/messaging/kafka_consumer.go](../../internal/infrastructure/messaging/kafka_consumer.go) (lines 27–29)

**Issue:** `GroupID: "payment-service-group-test"` and `Topic: "order.created"` are hardcoded strings. The `cfg *config.KafkaConfig` parameter is only used for broker address. The group ID has the suffix `-test`, suggesting this was never updated from a test value.

**Impact:** In production the consumer joins the wrong group, breaking offset tracking across deployments. Cannot be overridden without a code change.

**Fix:** Add `GroupID` and `Topic` fields to `config.KafkaConfig` and use them here.

---

### [MEDIUM] `SagaConsumer` retry map grows unbounded on repeated distinct offsets

**File:** [internal/infrastructure/messaging/saga_consumer.go](../../internal/infrastructure/messaging/saga_consumer.go) (line 43)

**Issue:** `retries := make(map[int64]int)` accumulates entries for every offset that is successfully processed (deleted on success) but also for offsets that are committed after max-retries. Between the `delete(retries, msg.Offset)` at line 76 and the one at line 68, entries for transiently-failing messages that eventually succeed are cleaned up correctly. However, if the consumer runs for a long time with many transient failures, the map can grow large. More seriously, the map is local to the goroutine's stack frame for each `Start()` call, meaning a consumer restart resets all retry counters — transient errors are never durably tracked.

**Impact:** Low memory pressure risk in practice, but retry semantics are weaker than advertised: a message that fails exactly 3 times every time the consumer restarts will never be DLQ'd.

**Fix:** Use a persistent retry counter (Redis INCR keyed on `topic:partition:offset`) so retry counts survive restarts.

---

### [MEDIUM] `outbox_relay.go`: context passed to `Unlock` on shutdown may already be done

**File:** [internal/application/outbox_relay.go](../../internal/application/outbox_relay.go) (line 53)

**Issue:** `r.mutex.Unlock(context.Background(), 1001)` correctly uses a fresh background context. However the error is silently discarded with `_ =`. If unlock fails (e.g., the DB connection is already torn down), the lock record remains and the log shows nothing.

**Fix:** Log the error: `if err := r.mutex.Unlock(...); err != nil { log.Errorw("outbox relay: failed to release lock on shutdown", "error", err) }`.

---

### [LOW] `kafka_publisher.go`: `Close()` error not checked at call sites

**File:** [internal/infrastructure/messaging/kafka_publisher.go](../../internal/infrastructure/messaging/kafka_publisher.go) (lines 38–40) and [internal/infrastructure/messaging/module.go](../../internal/infrastructure/messaging/module.go) (lines 25–27)

**Issue:** `module.go` correctly returns `pub.Close()` from the Fx `OnStop` hook, so Fx will log and handle the error. However the `Close()` implementation itself (`p.writer.Close()`) is a blocking flush — if it blocks longer than the Fx shutdown timeout, it is cancelled without warning, potentially losing buffered messages.

**Fix:** Add a context-aware close path or document the flush timeout. Consider wrapping `writer.Close()` with a `time.AfterFunc` alert if close takes longer than `WriteTimeout`.

---

### [LOW] `saga_compensator.go`: error from `HandleOrderFailed` is not wrapped with context

**File:** [internal/application/saga_compensator.go](../../internal/application/saga_compensator.go) (lines 42–45, 71–73, 84–85)

**Issue:** Errors are logged and returned unwrapped (`return err`, `return errUow`). The call stack from `SagaConsumer.Start` → `HandleOrderFailed` → `uow.Do` → inner closure loses all intermediate context for tracing.

**Fix:** Use `fmt.Errorf("saga compensator: rollback DB for order %d: %w", event.OrderID, errUow)` at each return site.

---

### [NIT] `KafkaConsumer.Start` returns `nil` on context cancellation but callers may not check

**File:** [internal/infrastructure/messaging/kafka_consumer.go](../../internal/infrastructure/messaging/kafka_consumer.go) (line 47)

**Issue:** Returning `nil` on graceful shutdown is correct semantics, but callers need to distinguish "stopped cleanly" from "never started". There is no structured shutdown acknowledgement.

---

## Test-Coverage Gaps (in-scope only)

- `SagaConsumer.Start` has **no unit tests**. The retry/DLQ path at lines 63–72 is completely untested.
- `KafkaConsumer.Start` has **no unit tests**. The `ProcessOrder` error + no-commit path (line 68) is untested.
- `saga_compensator.go` has no test for the **partial compensation failure** scenario (DB succeeds, Redis fails, retry behaviour).
- `outbox_relay_tracing.go` has no test verifying that span status reflects batch errors.
- `payment/service.go` has no test for the **zero-value OrderID silent drop** (line 43), so the silent-failure behaviour is undetected.

---

## Follow-Up Questions

1. Is the `order.failed.dlq` topic pre-provisioned in the Kafka config, or does `AllowAutoTopicCreation` need to remain enabled for it?
2. The Lua `revert.lua` script sets a SETNX idempotency key — does it set the key **before** or **after** the `INCRBY`? If before, a crash between SETNX and INCRBY permanently skips compensation with no alert.
3. Is there a plan to move the `retries` map to Redis (for cross-restart durability) before horizontal scaling, or is the current in-process map considered acceptable for a single-instance deploy?
4. The outbox relay uses `pg_try_advisory_lock(1001)` — is lock ID `1001` documented anywhere as a stable constant, or could a future migration accidentally reuse it?
5. `KafkaConsumer` group ID `"payment-service-group-test"` — is this intentionally the production value, or a leftover from development?

---

## Out of Scope / Deferred

- `worker_service.go` and `booking_service.go` DDD-level concerns — covered by dimension #1.
- PostgreSQL persistence layer (`outbox_repo`, `order_repo`) error handling — covered by dimension #3.
- Redis `deduct.lua` / `revert.lua` Lua script internals — covered by dimension #2 (concurrency/cache).
- HTTP handler layer validation and rate limiting — covered by dimension #1.
