# Review: Domain & Application Correctness

**Branch:** `review/domain-application` **Agent:** `go-reviewer` **Date:** 2026-04-11
**Scope:** internal/domain/**, internal/application/** (excl. payment)

## Summary

The domain model is clean, thin, and well-separated; aggregate boundaries are respected and all inventory mutations go through the repository layer rather than the entity. The main risks are concentrated in the application layer: a silently-swallowed `json.Marshal` error can corrupt the outbox, non-atomic dual-write in `CreateEvent` can leave Redis and Postgres inconsistent, and pervasive bare `return err` calls in `saga_compensator.go` and `worker_service.go` destroy call-chain context. A direct `err != context.Canceled` comparison should use `errors.Is`.

## Findings (severity-ranked)

### [HIGH] Ignored json.Marshal error silently corrupts outbox payload

- **File:** [internal/application/worker_service.go](../../internal/application/worker_service.go#L105-L106)
- **Issue:** `payload, _ := json.Marshal(order)` discards the error. If marshalling fails (e.g. unsupported type in a future Order field), `payload` is `nil`, and an outbox event with a nil/empty payload is committed inside the same UoW transaction as the order. The payment service will then receive a malformed message.
- **Impact:** Silent data corruption in the outbox; downstream payment consumer receives an empty payload and will fail or skip the order without a compensating path.
- **Fix:** Return the error: `payload, err := json.Marshal(order); if err != nil { return fmt.Errorf("marshal order for outbox: %w", err) }`

### [HIGH] Non-atomic dual-write in CreateEvent leaves Redis/Postgres inconsistent

- **File:** [internal/application/event_service.go](../../internal/application/event_service.go#L38-L47)
- **Issue:** `repo.Create` (Postgres) and `inventoryRepo.SetInventory` (Redis) are two separate calls with no rollback. If `SetInventory` fails after the DB row is committed, the event exists in Postgres but Redis holds no inventory. All subsequent `DeductInventory` calls will return `ErrSoldOut` immediately, effectively making the event unsellable without manual intervention.
- **Impact:** Production events silently go unsellable; no alerting path exists because `CreateEvent` returns an error to the caller but the DB row is already committed.
- **Fix:** Wrap both operations in a saga or use a compensating delete: if `SetInventory` errors, attempt `repo.Delete(ctx, event.ID)` and return a wrapped composite error. Alternatively document this as a known risk requiring an admin repair script, and add a startup reconciliation job.

### [MEDIUM] Bare `return err` throughout saga_compensator and worker_service loses call context

- **File:** [internal/application/saga_compensator.go](../../internal/application/saga_compensator.go#L44-L85), [internal/application/worker_service.go](../../internal/application/worker_service.go#L78-L124)
- **Issue:** Multiple `return err` statements return raw errors from repository calls without wrapping (e.g. at saga_compensator.go:44, 57, 65, 84; worker_service.go:78, 82, 97, 101, 115). The caller, typically a Kafka consumer or Fx lifecycle, receives an error with no indication of which step failed.
- **Impact:** Difficult to triage from logs alone; structured log fields are added but the error chain is severed, which defeats `errors.Is/As` matching further up the stack.
- **Fix:** Use `fmt.Errorf("unmarshal order failed event: %w", err)`, `fmt.Errorf("get order by id: %w", err)`, etc. at each return site.

### [MEDIUM] `err != context.Canceled` uses direct comparison instead of errors.Is

- **File:** [internal/application/worker_service.go](../../internal/application/worker_service.go#L62)
- **Issue:** `err != context.Canceled` does not unwrap wrapped context errors. If the queue's `Subscribe` wraps `context.Canceled` (e.g. `fmt.Errorf("subscribe: %w", context.Canceled)`), the guard fails and the worker logs a spurious error on clean shutdown.
- **Impact:** False-positive error logs on graceful shutdown; could trigger alerts in an observability pipeline.
- **Fix:** Replace with `!errors.Is(err, context.Canceled)`.

### [MEDIUM] CreateEvent has no input validation for empty event name

- **File:** [internal/application/event_service.go](../../internal/application/event_service.go#L26-L29)
- **Issue:** Only `totalTickets <= 0` is validated. An empty or whitespace-only `name` is persisted without complaint, producing events that are discoverable but not meaningful.
- **Impact:** Data quality; low blast radius but violates the "validate at all system boundaries" rule.
- **Fix:** Add `if strings.TrimSpace(name) == "" { return nil, fmt.Errorf("event name must not be empty") }`.

### [MEDIUM] Outbox relay hardcodes lockID 1001 in two places

- **File:** [internal/application/outbox_relay.go](../../internal/application/outbox_relay.go#L53-L57)
- **Issue:** `lockID 1001` appears both in `TryLock` (line 57) and `Unlock` (line 53) as bare magic numbers. They must stay in sync; a future edit changing one but not the other would cause the relay to never release its advisory lock.
- **Impact:** Advisory lock leak on graceful shutdown or mismatch-introduced deadlock.
- **Fix:** Define `const outboxAdvisoryLockID int64 = 1001` in the file or package and reference it at both call sites.

### [LOW] Event.Deduct mutates receiver instead of returning a new value

- **File:** [internal/domain/event.go](../../internal/domain/event.go#L29-L38)
- **Issue:** `Deduct` modifies `e.AvailableTickets` in-place on the pointer receiver. Per the project's immutability contract ("create new objects, never mutate"), the method should return a new `Event` value with updated fields.
- **Impact:** Although `Deduct` is currently only exercised in unit tests (not called in the hot path), it sets a precedent that could lead to unintended mutations if reused in future application code.
- **Fix:** Return `(Event, error)` and construct a new struct: `return Event{…, AvailableTickets: e.AvailableTickets - quantity}, nil`.

### [LOW] SagaCompensator constructor silently drops logger if zap global is uninitialized

- **File:** [internal/application/saga_compensator.go](../../internal/application/saga_compensator.go#L36-L37)
- **Issue:** `zap.S().With(...)` returns a no-op logger if the global zap logger has not been initialized (e.g. in unit tests or early startup). Errors will be silently discarded in those contexts.
- **Impact:** Missing log output during testing, no runtime error, but misleading for future test authors.
- **Fix:** Accept `*zap.SugaredLogger` as a constructor parameter (consistent with `workerService`) rather than reading the global.

### [LOW] EventRepository interface retains deprecated method

- **File:** [internal/domain/event.go](../../internal/domain/event.go#L44)
- **Issue:** `DeductInventory` is annotated `// Deprecated in favor of Lifecycle, but kept for legacy` but remains in the interface. Every mock and implementation must still satisfy this method, increasing maintenance surface.
- **Impact:** Interface pollution; any new implementation must stub a method that should not be called.
- **Fix:** Remove `DeductInventory` from the interface in a follow-up PR, replacing legacy call sites with `DecrementTicket`.

### [NIT] Error message in CreateEvent violates Go style (not wrapped, no %w)

- **File:** [internal/application/event_service.go](../../internal/application/event_service.go#L28)
- **Issue:** `fmt.Errorf("total tickets must be positive")` does not wrap an underlying cause (no `%w`). This is fine for a sentinel-style validation error, but the message uses a full sentence rather than a lowercase fragment without trailing punctuation, violating the Go error string convention.
- **Fix:** `fmt.Errorf("total tickets must be positive")` → `errors.New("total tickets must be positive")` (no fmt needed; no cause to wrap).

### [NIT] module.go uses context.Background() for OutboxRelay lifetime

- **File:** [internal/application/module.go](../../internal/application/module.go#L32)
- **Issue:** `context.WithCancel(context.Background())` creates a root context detached from the Fx shutdown context passed to `OnStop`. This is intentional here (cancel is called in OnStop), but the pattern is fragile — if someone adds a timeout to the `OnStop` hook, the relay will not inherit it.
- **Fix:** Pass the Fx `lc` stop context through if Fx exposes it, or add a comment documenting why the detached context is necessary.

## Test-Coverage Gaps (in-scope only)

- `saga_compensator.go` has no test file. The idempotency guard (`order.Status == OrderStatusCompensated`) and the Redis revert step are completely untested.
- `event_service.go` has no test file. The non-atomic dual-write path and the `totalTickets <= 0` guard are untested.
- `booking_service_test.go` uses `assert.EqualError` for the "Redis Error" case instead of `errors.Is`, which means a wrapped error with different message text would break the test even if the sentinel is correct.
- `outbox_relay_test.go` does not test the `runWithBatchHook`/leader election path; only `processBatch` is covered.
- `worker_service_test.go` accesses the unexported `workerService` struct directly (`svc := &workerService{…}`) from within the same package, which bypasses the public constructor and could mask constructor-level validation in the future.

## Follow-Up Questions

1. Is `Event.Deduct` intended to remain in the domain entity long-term? If the hot path exclusively uses `InventoryRepository`, should `Deduct` be removed to avoid confusion?
2. Is there a startup reconciliation mechanism to detect Redis/Postgres inventory drift introduced by the `CreateEvent` dual-write window? If not, is one planned?
3. The `OutboxRepository.ListPending` has no upper bound on event age — is there a TTL or cleanup job for stale `PENDING` records that are stuck due to permanent publish failures?

## Out of Scope / Deferred

- `internal/application/payment/` — reviewed by dimension 5.
- Infrastructure adapters (Redis Lua scripts, Postgres repositories, Kafka consumer) — reviewed by other dimensions.
- API handler input validation and HTTP-layer idempotency — out of this dimension's scope.
