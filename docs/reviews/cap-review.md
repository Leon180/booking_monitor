# CAP & Consistency Model Review

**Reviewer**: Backend Architect (Claude Opus, project audit 2026-05-27)
**Scope**: CAP trade-offs, dual-tier inventory consistency, outbox + saga correctness, idempotency
**Go version pinned**: 1.25.0 / toolchain 1.25.10

---

## TL;DR

- **[MAJOR-1]** `pgAdvisoryLock` (the outbox leader-election lock) caches `*sql.Conn` and only relinquishes it on `Unlock`. If the cached connection silently breaks (network reset / PG primary failover / PgBouncer transaction-pool drop), the relay process believes it still holds the lock — the in-memory `wasLeader=true` short-circuit at `internal/infrastructure/persistence/postgres/advisory_lock.go:29-32` returns `true` without re-validating. Two relay replicas can simultaneously hold their cached "I am leader" view, each polling the outbox and double-publishing events. Combined with delivery being explicitly at-least-once + no dedupe header on outbox events, this widens an already-at-least-once contract into a partition-time double-publish window. ([Stormatics — PostgreSQL split-brain](https://stormatics.tech/blogs/understanding-split-brain-scenarios-in-highly-available-postgresql-clusters); [Kerkour — Advisory locks leader election](https://kerkour.com/postgresql-leader-election-advisory-lock))
- **[MAJOR-2]** Outbox poller uses a naïve `ListPending` then `MarkProcessed` per event — no `FOR UPDATE SKIP LOCKED`, no lease, no batch claim. The `pg_try_advisory_lock(1001)` serialises the *normal* case to one relay, but the moment that lock is unsafe (see MAJOR-1), nothing else prevents two relays from re-publishing the same outbox row before either marks it processed. Industry consensus in 2025 is "advisory lock + `SKIP LOCKED` claim, not advisory lock alone". ([AWS Prescriptive Guidance — Outbox](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html); [Software Craftsperson — Outbox trade-offs 2025](https://www.softwarecraftsperson.com/posts/2025-10-08-transactional-outbox-pattern/))
- **[MAJOR-3]** Booking-path RYW violation under PEL retry: the API returns `202` with `order_id`, the client polls `/orders/:id`, gets `404` (documented honestly in `CLAUDE.md` and [`booking/service.go:51-56`](../../internal/application/booking/service.go)), retries, finally sees `awaiting_payment`. So far so good. But on a worker crash + PEL redelivery, the inserted row's `status` is unchanged but its `created_at` / `updated_at` shift, the saga path can compensate the same `order_id` if the message had previously made it to DLQ, and there is no order-level "monotonic read" cursor — a client polling against a stale read replica (no replicas today, but the design already invites this) could observe `awaiting_payment → 404 → awaiting_payment` if the row was rolled back inside the UoW and re-inserted on retry. Not currently buggy — but the "client + server share the same id pre-202" pattern is sound and the rest of the guarantees are weaker than the surface API suggests.
- **[MAJOR-4]** `payment_failed` webhook IMMEDIATELY drives saga compensation rather than keeping the order in `awaiting_payment` for retry within `reserved_until`. This is already tracked in the user's memory file (`payment_retry_design_gap.md`) — confirmed against `internal/application/payment/webhook_service.go:482-538` (`handleFailure`). Industry SOP (Stripe Payment Intents, Adyen) is to allow N retry attempts inside the reservation window before declaring failure. The current design surfaces a CAP-relevant correctness wart: a transient gateway 5xx that Stripe retries (`payment_intent.payment_failed` → `payment_intent.succeeded`) lands as `payment_failed → MarkPaymentFailed → compensator reverts Redis → late succeeded webhook → manual refund`. Loss is not silent (`isFailureTerminalAfterPay` log line covers it) but the design assumes the gateway is point-in-time deterministic, which it isn't.
- **[MINOR-1]** Idempotency middleware races: 3-op (Get → handler → Set) is non-atomic with the Lua deduct inside the handler. Already tracked + accepted in `docs/architectural_backlog.md` (2026-05-12 entry, "3-op Redis pattern + cache memory cap"). The mitigation (server-minted UUIDv7 + DB UNIQUE) is real and the over-deduction is bounded — Redis-side drift only, no double-booked orders. Tracking plan still adequate.
- **[NIT-1]** Two surfaces overstate guarantees: the `OrderQueue` Subscribe doc reads "queue manages retry / ACK / DLQ" without naming at-least-once explicitly; the Lua deduct comment says "atomic" — true for the Lua execution itself, but the encompassing Lua + XADD + worker tx + PG INSERT chain is NOT atomic and the rest of the codebase does not claim it is. Hold the line; do not weaken the existing honest framing.

---

## 1. Per-subsystem CAP stance

| Subsystem | CAP stance | Partition-tolerance mechanism | Known failure modes |
| --- | --- | --- | --- |
| Booking hot path (Lua deduct → Stream → worker → PG INSERT) | **AP at the API edge, eventually consistent toward PG** | Lua is single-threaded inside Redis (no quorum), XADD is part of the same Lua atomic; PEL retry redelivers a crashed worker's claim; revert.lua is idempotent on `compensationID` | Redis up + worker crash mid-tx → PEL retry; Redis up + PG down → XADD succeeded but `Process` rolls back, message goes back to PEL; Redis crash with `appendonly no` (current default) → all pending reservations lost, must rehydrate from PG (`docs/architectural_backlog.md` Cache-truth entry, 2026-05-03) |
| Outbox relay | **CP-leaning at the writer (advisory lock serialises) but AP-shaped under PG primary failover** | `pg_try_advisory_lock(1001)` for leader election; per-event publish without `MarkProcessed` is intentionally retry-safe; consumers documented at-least-once | MAJOR-1 cached-conn split-brain; MAJOR-2 missing `FOR UPDATE SKIP LOCKED` claim; "publish but MarkProcessed fails" path (`relay.go:142-149`) explicitly re-publishes — at-least-once delivery |
| Saga compensator | **AP — designed for idempotent at-least-once delivery** | Two-phase fence: PG `order_compensations.compensation_id` UNIQUE + Redis `revert.lua` EXISTS-then-SET guard with 7-day TTL; `WasRedisReverted` / `MarkRedisReverted` survive Redis crash via PG (Seata TCC-style fence pattern) | `appendfsync=always` + Redis crash → INCRBY persisted, SET lost → second pass over-reverts (loud, alertable); ticket_type resolution Path C (legacy + multi-type) skips DB increment but still ACKs the order — documented operator-visible counter |
| Recon (charging two-phase) | **CP-leaning for the resolution decision** | Read-only `PaymentStatusReader` interface segregates from `Charge`; reads gateway, MarkConfirmed/MarkFailed via UoW; failOrder emits `order.failed` in same UoW (DEF-CRIT fixed, PR #51) | Null `payment_intent_id` is skipped + metric'd (orphan triage); transient gateway error → skip + retry next sweep; max-age force-fail emits `order.failed` so saga reverts Redis — closed |
| Expiry sweeper (D6) | **AP — designed to always expire eligible rows** | Same UoW pattern as recon: MarkExpired + outbox emit atomic; saga SETNX guard makes re-emit idempotent; DB-NOW as single time source (round-2 F1 contract); MaxAge rows are still expired with distinct `expired_overaged` label | D5 webhook can win race inside UoW (`ErrInvalidTransition` → `already_terminal` label); status-already-non-awaiting pre-UoW check is best-effort, the UoW row-lock is the real arbiter |
| Idempotency cache | **AP, fail-open** | Redis-only, 24h TTL, Stripe-style hex-SHA256-of-body fingerprint, 3-way switch (match/legacy/mismatch); cache GET errors → process as cache-miss (idempotency briefly suspended) | 3-op non-atomic (MINOR-1 / tracked); no user scoping (defensible / known); response body cached verbatim, can include PII if 2xx body contained it |

---

## 2. Dual-tier inventory consistency — trace + failure modes

### 2.1 Happy path

1. Handler validates → `BookingService.BookTicket` ([`booking/service.go:97-178`](../../internal/application/booking/service.go))
2. UUIDv7 minted at line 108 (so id is stable across PEL retries)
3. `inventoryRepo.DeductInventory` ([`cache/redis.go:308-397`](../../internal/infrastructure/cache/redis.go)) → Lua `deduct.lua` does (a) DECRBY inventory_key (b) HMGET metadata_key (c) compute amount_cents in arbitrary precision (d) XADD `orders:stream` — all atomically inside Redis Lua single-threaded execution
4. Handler returns 202 with `order_id` + `reserved_until`
5. Worker consumes via `XREADGROUP` ([`cache/redis_queue.go:189-260`](../../internal/infrastructure/cache/redis_queue.go))
6. `orderMessageProcessor.Process` ([`worker/message_processor.go:65-160`](../../internal/application/worker/message_processor.go)) runs UoW: `ticket_type.DecrementTicket` + `order.Create` — atomic in PG tx
7. XAck → message removed from PEL
8. Admin event published post-commit (non-blocking; drop-on-full)

**Bounded convergence**: Sub-second under healthy conditions. The DB row exists "ms after 202" — documented honestly at `booking/service.go:51-56`.

### 2.2 Failure mode — Lua deduct succeeds but XADD fails inside same Lua

**Cannot happen**. Both DECRBY and XADD are inside the same Lua script (`deduct.lua` lines 14 + 87-95). Redis Lua is atomic — either both commit to the in-memory dataset together or neither does. The `appendfsync=everysec` (project default) durability story is well-defined: either the whole script's effects make AOF together on the next fsync, or they're all lost on crash. Loud-over-revert beats silent-under-revert, per the comment block in `revert.lua:7-19`.

**Convergence**: N/A — outcome is binary.

### 2.3 Failure mode — Worker crashes mid-tx (after XREADGROUP, before XACK)

1. PEL retains the message (claim held by dead consumer)
2. On next worker start, `processPending(ctx, consumerName, handler)` ([`cache/redis_queue.go:759-851`](../../internal/infrastructure/cache/redis_queue.go)) replays with ID "0"
3. Handler re-runs with the **same `orderID`** (reused from message body, not re-minted)
4. DB UNIQUE on `orders.id` catches duplicate on `Create` (`domain.ErrUserAlreadyBought` path at `message_processor.go:110-113`)
5. After max retries → handleFailure → `RevertInventory` (idempotent via `revert.lua` EXISTS guard on the **msgID**, not the orderID — `redis_queue.go:488`) → DLQ → XACK

**Convergence**: Bounded by `WORKER_MAX_RETRIES * WORKER_RETRY_BASE_DELAY * N` (default 3 × 100ms linear backoff = ~600ms compensation budget) + `WORKER_FAILURE_TIMEOUT` (default 5s). Evidence on disk: `orders` row exists with original status, `events_outbox` DOES NOT have an `order.failed` row from this path (compensation goes through Lua revert + DLQ, NOT through saga), Redis inventory restored, DLQ entry written with `reason=exhausted_retries` or `malformed_classified`.

⚠️ **Gap**: The compensation key in `revert.lua` is `saga:reverted:{msgID}` — but `msgID` is the **Redis stream id** (`<timestamp>-<seq>`), NOT the order id. After PEL recovery, the same logical retry uses the same stream id, so idempotency holds. But if a future change ever splits "PEL recover" from "DLQ direct revert" with different msgID semantics, this guard breaks. Not currently buggy; document the assumption.

### 2.4 Failure mode — PG up but Redis down (mid-booking)

If Redis drops between handler entry and Lua run, the booking call returns a wrapped error ([`booking/service.go:175-177`](../../internal/application/booking/service.go)) → handler returns 5xx → client retries. **No inventory drift** because the Lua never ran. Idempotency middleware short-circuits early via `cacheGetFailed` (fail-open at `idempotency.go:263-272`).

If Redis dies AFTER successful Lua but BEFORE worker reads the stream, the message is in Redis memory (`appendfsync=everysec`) — within 1 second of fsync it's durable. If crash is within that 1-second window: the inventory deduct AND the stream message are both lost together. Rehydrate from PG on restart (rehydrate path at `cache/rehydrate.go`). Status of any unmint'd order: client polls `/orders/:id` → 404 forever, must call user-support. This is documented as the trade-off the project accepts (`docs/adr/0001_async_queue_selection.md`: "the durability risk of Redis (with AOF enabled) is acceptable compared to the operational overhead of Kafka").

⚠️ **2026 industry counterpoint**: Shopify migrated their reservation pool **off Redis and onto MySQL** ([Shopify Engineering 2026](https://shopify.engineering/scaling-inventory-reservations)) precisely because the durability story for inventory reservations is hard to reason about. Not a regression in this codebase — the project explicitly markets itself as "flash-sale simulator" and the trade-off is deliberate and documented. Worth surfacing in interview defence: "we made the opposite choice from current Shopify, and we know why".

### 2.5 Failure mode — Redis cluster split (NOGROUP self-heal)

This was the 2026-05-03 finding referenced in `architectural_backlog.md` — `redis_queue.go:220-251` self-heals via `XGROUP CREATE … $`, which silently skips messages that arrived between the destruction and the recovery. Now alertable via `RecordConsumerGroupRecreated` + `ConsumerGroupRecreated` rule. The mitigation plan (rehydrate-from-DB PR-B, NOGROUP alert PR-C, drift reconciler PR-D) is sound. **Convergence**: not automatic — operator-driven via the alert + rehydrate.

### 2.6 Failure mode — cross-region replica lag

Not applicable today (no replicas). The runtime metadata design (`docs/design/redis_runtime_metadata_scaling.md`) is explicitly aware that "if the system later needs Redis Cluster or app-level sharding, the next design step is about reshaping keys and queues" — including hash-tagging keys (`ticket_type_meta:{<id>}`) and avoiding the global `orders:stream` inside the atomic call. Tracked, not done.

---

## 3. Outbox advisory lock — split-brain analysis

### The advisory-lock implementation

`pgAdvisoryLock.TryLock` ([`postgres/advisory_lock.go:24-53`](../../internal/infrastructure/persistence/postgres/advisory_lock.go)) takes a long-lived `*sql.Conn` and runs `SELECT pg_try_advisory_lock($1)` on it. The lock is **session-scoped** — released automatically when the connection closes. `Unlock` runs `pg_advisory_unlock($1)` and closes the connection.

The relay's caller pattern ([`outbox/relay.go:88-100`](../../internal/application/outbox/relay.go)) calls `TryLock` on every ticker tick. On line 29-32 of advisory_lock.go:

```go
// If we already hold the connection (and thereby the session lock),
// we are currently the leader. No need to query the DB again.
if l.conn != nil {
    return true, nil
}
```

This is the load-bearing optimisation — avoid round-tripping `pg_try_advisory_lock($1)` on every 500ms tick. Cost: trust the in-memory cache.

### The split-brain failure modes

**Mode A — PG primary failover** (HA setup, not deployed today but the design pretends it's safe):
1. Replica A's relay holds the lock on PG primary `pg1`
2. `pg1` becomes unreachable; Patroni / RDS failover promotes `pg2`
3. The TCP connection on Replica A breaks — but `l.conn != nil` still returns true on the next tick
4. Replica B starts after failover, gets a fresh `sql.Conn` against `pg2`, `pg_try_advisory_lock(1001)` returns **true** (the old session on `pg1` is gone)
5. Both A and B think they're leader until A's next `processBatch` call fails on the dead conn

**Mode B — PgBouncer transaction pooling**:
The advisory-lock pattern documented at [Kerkour's leader-election post](https://kerkour.com/postgresql-leader-election-advisory-lock) explicitly warns: "Advisory locks do not work reliably with PgBouncer when it is in transaction pooling mode, as connections are frequently swapped between clients". Today there's no PgBouncer in the stack (`docker-compose.yml` connects app → `postgres` directly), but the design has no guard against this.

**Mode C — network blip without close**:
TCP can hang for minutes on a half-open connection without the Go driver knowing. During that window the cached `*sql.Conn` reports "I have the lock" but the PG server has already released it (after `tcp_keepalive` triggers, typically 2h on Linux default).

### What happens if two relays publish the same event?

Look at `processBatch` ([`relay.go:117-153`](../../internal/application/outbox/relay.go)):

```go
if err := r.publisher.PublishOutboxEvent(ctx, e); err != nil {
    // ... continue
}
if err := r.outboxRepo.MarkProcessed(ctx, e.ID()); err != nil {
    // Intentional: the event was already published. If MarkProcessed
    // fails, it will be re-published on the next tick. Consumers must
    // be idempotent (at-least-once delivery guarantee).
}
```

Two relays both reading the same `events_outbox` row, both publishing to Kafka, both attempting `MarkProcessed`. The MarkProcessed itself is idempotent (UPDATE `processed_at`), so the DB stays consistent. But Kafka now has the same `order.failed` event twice with the same `event_id`.

**Saga compensator behaviour**: The saga uses `compensationID = "order:" + event.OrderID` ([`saga/compensator.go:163`](../../internal/application/saga/compensator.go)) as the SETNX key in `revert.lua`. So a double-published `order.failed` for the same order will: first delivery → revert + MarkCompensated + RecordCompletion; second delivery → `wasAlreadyCompensated=true`, goes through `WasRedisReverted` check, skips revert. **Idempotency holds end-to-end thanks to the Seata-TCC-style fence pattern.** This is genuinely strong design.

### Verdict

The saga downstream is genuinely idempotent and survives the split-brain double-publish. But:
1. The relay's "I am leader because `l.conn != nil`" check is structurally weak. The correct pattern is to re-validate with a cheap `SELECT 1` or `SELECT pg_try_advisory_lock` on each tick — the latter is a no-op when you already hold it and a real check when you don't. Two relays simultaneously believing they're leader for several ticks isn't currently a correctness bug, but it WILL be the day someone adds a non-idempotent consumer or splits the saga step into one-time effects (e.g. emailing the customer "your booking failed").
2. There is no `FOR UPDATE SKIP LOCKED` in `ListPending`, which is the 2025-industry-standard belt-and-braces approach ([AWS Prescriptive Guidance](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html), [Microservices.io](https://microservices.io/patterns/data/transactional-outbox.html), [Software Craftsperson 2025](https://www.softwarecraftsperson.com/posts/2025-10-08-transactional-outbox-pattern/)).
3. Documentation overstates safety. `relay.go:14-20` says the advisory lock is "leader election across all Relay replicas. Defined once to avoid the historical magic-number-in-two-places bug" — true, but the "across all replicas" claim assumes the cache check is sound, which (1) shows it isn't.

---

## 4. Saga / Recon max-age force-fail — current state of DEF-CRIT

### Phase 2 finding (verified)

Per [`docs/checkpoints/20260430-phase2-review.md`](../checkpoints/20260430-phase2-review.md) §8 (DEF-CRIT): "Reconciler max-age force-fail leaks Redis inventory. Verified: `MarkFailed` (`postgres/repositories.go:465`) only calls `transitionStatus` … it does not write to `events_outbox`. The reconciler's max-age branch calls `MarkFailed` directly. No `order.failed` Kafka event is emitted."

### Current state (2026-05-27 audit)

**FIXED, verified.** [`recon/reconciler.go:342-380`](../../internal/application/recon/reconciler.go) — `failOrder` now runs **inside a UoW**, MarkFailed + Outbox.Create atomic:

```go
func (r *Reconciler) failOrder(ctx context.Context, id uuid.UUID, reason string) error {
    order, err := r.orders.GetByID(ctx, id)
    if err != nil { ... }
    return r.uow.Do(ctx, func(repos *application.Repositories) error {
        if err := repos.Order.MarkFailed(ctx, id); err != nil { return err }
        failedEvent := application.NewOrderFailedEventFromOrder(order, reason)
        payload, err := json.Marshal(failedEvent)
        if err != nil { ... }
        outboxEvent, err := domain.NewOrderFailedOutbox(payload)
        if err != nil { ... }
        if _, err := repos.Outbox.Create(ctx, outboxEvent); err != nil { ... }
        return nil
    })
}
```

All three Charging→Failed branches (max_age, declined, not_found) route through this helper. The `architectural_backlog.md` HTML comment at line 229-232 records the resolution: "VERIFIED FIXED (PR #51 / CP1). `reconciler.failOrder()` runs MarkFailed + outbox.Create inside a single UoW.Do, so the saga compensator is correctly triggered on force-fail."

### Saga watchdog max-age — by-design asymmetric

[`saga/watchdog.go:178-198`](../../internal/application/saga/watchdog.go) — when a `failed` order exceeds `SAGA_MAX_FAILED_AGE`, the watchdog **does NOT auto-transition** to Compensated. Code comment makes the rationale explicit:

> "Unlike the reconciler's force-fail path, the watchdog cannot safely auto-transition: Failed → Compensated requires Redis inventory to be actually reverted (otherwise we have a phantom-revert state-machine corruption). The right answer is to alert and let an operator decide."

This is correct. The asymmetry is defensible: `Charging` can be force-failed because Redis inventory was already deducted at booking time AND the outbox emit triggers the saga AND the saga handles "fresh compensation" idempotently. `Failed` already had its compensation chance — auto-transitioning would skip the inventory-revert verification step.

### Verdict

DEF-CRIT closed. The 2026-05-27 implementation has the right shape. No new gap.

### One worth checking forward

`recon/reconciler.go:362` synthesises the failed event with `order` re-used from a pre-tx `GetByID`. The comment defends staleness: "EventID, UserID, Quantity — write-once post-creation, so staleness is correctness-safe". True today; if a future migration adds a mutable field used by `NewOrderFailedEventFromOrder`, this assumption silently rots. Worth a runtime check or a domain-level guard.

---

## 5. Read-your-writes audit

### The documented 404 window

`CLAUDE.md` (paraphrased from the API endpoints section): "GET /api/v1/orders/:id … returns 404 during the brief async-processing window". `booking/service.go:51-56`: "may return 404 for ~ms after 202 (async-processing window) and clients should retry with backoff".

This is **honest**. The id is minted at the API boundary (line 108), so the client has a real, stable id immediately. The 404 is a known, bounded artifact of the async architecture, not a correctness gap.

### Subtler RYW violations

**A. Status non-monotonicity under PEL redelivery.** Walk the path:

1. Worker A receives stream message, opens tx, INSERT order → row visible to other tx as `awaiting_payment`
2. Worker A's tx rolls back (e.g. DB connection drops during commit) → row disappears
3. Client polls between (1) and (2) → sees `awaiting_payment` (within Read-Committed isolation, transactional visibility depends on commit; under Repeatable Read the row is invisible until commit)
4. Worker A's PEL claim expires, Worker B reclaims → INSERT order again → row visible as `awaiting_payment`

The client never sees a logical regression (`paid → reserved` or `reserved → 404`) under standard PG isolation — uncommitted inserts aren't visible. **No violation in PG.** But Redis-side observers can see a different picture: the inventory was deducted in step (1) and **NOT reverted** during step (2)'s rollback (the Lua deduct already committed in Redis; the tx rollback is only PG). The worker's `handleFailure` only fires on max-retry exhaustion, not on a single tx rollback. So if the retry succeeds in step (4), inventory accounting is fine; if it doesn't, inventory drift until DLQ.

**B. Polling consistency.** No `Last-Modified` / `ETag` / version cursor on `/orders/:id`. A future replica-read architecture would let the client see `awaiting_payment` from a stale replica AFTER the primary already wrote `paid`. Not a today-bug; the design hasn't built in the cursor that would close this gap. A read-replica cutover would need a version column on `orders` and a client-passed `If-Match` semantic.

**C. PEL retry order divergence.** Pre-PR #47 (memory: `feedback_run_full_tests_before_pr.md` references), the worker minted its own UUID per retry, so client-id and DB-id diverged. **Fixed**; id flows end-to-end. Strong design.

### Verdict

RYW correctness is fundamentally honest. The 404 window is documented. The pre-PEL-retry-id-divergence bug was caught and fixed in PR #47. The only structural risk is the future read-replica path, which the design hasn't started to address.

---

## 6. Idempotency-Key + body-fingerprint race audit

### The implementation under scrutiny

[`middleware/idempotency.go:178-318`](../../internal/infrastructure/api/middleware/idempotency.go) implements the Stripe-style 3-way switch:

- hit + matching fingerprint → replay
- hit + different fingerprint → 409 (correct per [Stripe's contract](https://docs.stripe.com/api/idempotent_requests))
- hit + empty fingerprint → legacy entry, replay + lazy write-back
- miss → call handler; on 2xx, Set the response with fp

Fingerprint = `hex(SHA-256(raw_body_bytes))` (line 96-99). Honest design choice not to canonicalise JSON ([Stripe blog on idempotency](https://stripe.com/blog/idempotency) — they recommend "byte-identical retries"); the comment block at lines 86-95 cites this explicitly.

### Race window 1 — concurrent same-key, same-body

Two requests arrive simultaneously with the same `Idempotency-Key` and same body:
1. Both call `repo.Get(ctx, key)` — both return cache miss (no entry yet)
2. Both call handler → both attempt `DeductInventory` → Lua atomically serialises them, one wins, the other gets sold_out (or, for non-sold-out paths, both succeed — different `order_id`s because UUIDv7 is request-scoped)
3. Both call `repo.Set(setCtx, key, ...)` — the second overwrites the first

**Outcome**: Two orders created, only one cached response. Subsequent retries see the SECOND response. **Idempotency contract is violated** — but Redis-side inventory is correctly deducted twice (correctness for the seller).

The mitigation (server-minted UUIDv7 + DB UNIQUE) is at the **order** level, not the **Idempotency-Key** level. A client retrying its own POST with the same key WILL get the cached second response after the racey period, which is the intent of the key. But two distinct clients colluding on the same key (the threat model the user already knows about; flagged as "no user scoping" in Phase 2 #4 challenge response and `architectural_backlog.md`) can produce 2 charges from "1 key".

### Race window 2 — concurrent same-key, different-body

1. R1 (body A): Get → miss; handler runs → Set(A, fp_A)
2. R2 (body B): arrives between R1's Get and R1's Set → Get → miss → handler runs → Set(B, fp_B) overwrites
3. R1's Set runs AFTER R2's Set → overwrites back to (A, fp_A)
4. R3 (body B retry): Get → returns (A, fp_A); fp_B ≠ fp_A → 409

The 409 is misleading — R3 IS the legitimate retry of R2, but appears to "conflict" with a request it never sent. Operator sees this in the log line at idempotency.go:254-257.

**Mitigation today**: Stripe docs the same gap and accepts it ("if the request was incomplete, retry with the same key will still race"). Not a regression vs Stripe.

### Race window 3 — fail-open after recovery

`getErr != nil` (line 263) → log + fall through to handler. `cacheGetFailed = true` (line 218) → suppress Set after handler (line 295: `if !shouldCacheStatus(statusCode) || cacheGetFailed { return }`). **Correct.** A flaky-then-recovered Redis cannot pin a transient response for 24h.

### Race window 4 — `c.GetRawData` vs body-size middleware ordering

Comment at line 165: "The body-size middleware MUST run before this so MaxBytesReader is in place". Verified — in production registration order (need to check the router wiring but the contract is explicit + tested). Not a bug today; documented contract.

### Verdict

Body-fingerprint design is sound and matches industry contract. Race window 1 is real but the deduct-side mitigation (UUIDv7 + DB UNIQUE) bounds it to "Redis drift, not double-booked orders". Honest acknowledgement of the no-user-scope gap is in `Phase 2 #4` defence. The middleware itself does NOT use SETNX (which would close window 1 but make the cache write 3-op atomic) — the project has explicitly considered and accepted this. Tracking memo at `architectural_backlog.md` (2026-05-12 entry) is adequate.

**Improvement worth considering for the next iteration**: Stripe and AWS Powertools both use a Lua-scripted "SETNX-and-claim-or-replay" pattern that collapses Get + handler-trigger + Set into a single atomic operation by using a "processing" sentinel value. The cost is per-endpoint Lua + a separate set of edge cases (handler crashes leave the sentinel; need TTL on the sentinel). The benefit is closing window 1. The project's current judgement is "cost > benefit at our scale" — defensible.

---

## 7. Honesty audit — promises vs delivery

### Promises that are honest

- `CLAUDE.md` Pattern A reservation semantics — accurate. Reservation in Redis, not auto-charged, worker writes `awaiting_payment`, 15min TTL, payment-intent flow.
- `BookingService.GetOrder` doc: "may return 404 for ~ms" — accurate.
- `revert.lua:7-19` crash semantics — explicitly enumerates appendfsync=everysec vs appendfsync=always cases. Honest.
- Saga compensator outcome labels — distinguishes `already_compensated`, `path_c_skipped`, `redis_revert_error`, `already_compensated_redis_mark_failed` etc. Operator can triage from the metric alone.
- `relay.go:137-149` — explicit "at-least-once delivery" comments on both publish-fail and mark-processed-fail paths.

### Subtle overstatements

**S1.** `outbox/relay.go:14-20` says the advisory lock is "leader election across all Relay replicas". Strictly true when `pgAdvisoryLock`'s cached-conn assumption holds, which under PG failover / network blip / PgBouncer it doesn't (see MAJOR-1). Tighten the doc to say "leader election, modulo the connection-loss caveats documented in `pgAdvisoryLock`".

**S2.** `deduct.lua` description (in `redis_runtime_metadata_scaling.md` §5): "one inventory decrement, one metadata fetch, small string/integer transformations, one stream publish" — accurate. The phrase "atomic" is appropriately scoped to the Lua execution itself, not the encompassing chain. Hold the line.

**S3.** `OrderQueue.Subscribe` doc ([`worker/queue.go:99-105`](../../internal/application/worker/queue.go)): "the queue manages retry / ACK / DLQ" — doesn't name at-least-once explicitly. The implementation IS at-least-once (PEL semantics + idempotent revert.lua), but a future maintainer reading just the interface could believe stronger. Worth a one-line addition.

**S4.** No claims of "exactly-once" anywhere in the codebase — searched. Honest. Industry consensus per [Medium / Yamishift 2025](https://medium.com/@komalbaparmar007/node-js-redis-streams-exactly-once-event-pipelines-with-consumer-groups-33318dfbd591): "Most 'exactly-once' pipelines in production are really at-least-once pipelines wearing a nice jacket". The codebase doesn't wear the jacket — good.

**S5.** `docs/scaling_roadmap.md` Stage 4 "Geo-distributed Events" is aspirational, not yet designed. The doc itself is honest about being a roadmap; only matters if external audiences mistake roadmap-as-current.

---

## Findings (numbered, sorted by severity)

### [MAJOR-1 → NIT-deferred pending Phase 4] Outbox advisory-lock cached-conn assumption is structurally weak under PG failover

> **Severity downgraded by round-2 meta-review + final-decisions D5**: the v1.0.0 stack has no PG replica and no PgBouncer transaction pooling, so the architectural risk described below is real but not reachable today. Re-promote to MAJOR when Phase 4 HA work introduces either dependency. See [`final-decisions.md` § D5](final-decisions.md) for the authoritative classification. The code-path analysis and recommendations are retained as a Phase-4 prep reference.

**Where**: [`internal/infrastructure/persistence/postgres/advisory_lock.go:24-53`](../../internal/infrastructure/persistence/postgres/advisory_lock.go)

**What**: `TryLock` short-circuits with `return true, nil` whenever `l.conn != nil`, never re-validating the cached connection. Under PG primary failover, PgBouncer transaction pooling, half-open TCP connections, or any path that breaks the underlying `*sql.Conn` without surfacing a Go-side error, two relay replicas can simultaneously believe they hold the lock. Combined with the existing at-least-once delivery contract and the absence of `FOR UPDATE SKIP LOCKED` on `ListPending` (MAJOR-2), this creates a window for double-publishing the same outbox row.

**Why it matters**: Today's saga downstream (Seata-TCC-style fence: PG `order_compensations.compensation_id` UNIQUE + Redis `revert.lua` EXISTS guard) is robust against double-published `order.failed` events. The system survives the split-brain double-publish today. But this is downstream protection masking an upstream weakness. Any future non-idempotent consumer of the outbox (email notifications, audit-log emit, third-party webhook) will not have the same protection, and the failure will be silent — both relays publish the event, the downstream takes the action twice, no metric signal except in the consumer.

**Recommendation**:
1. Inside `TryLock`, when `l.conn != nil`, run a cheap `SELECT 1` ping (with the existing ctx) and treat any error as "lock lost — reset". Cost: one round-trip per tick (every 500ms), ~negligible vs a `pg_try_advisory_lock` call.
2. Alternatively, drop the cache entirely and re-run `pg_try_advisory_lock($1)` every tick — the PG function is a no-op when you already hold the lock, returns true cheaply.
3. Independently, add `FOR UPDATE SKIP LOCKED` to the outbox `ListPending` query — belt-and-braces, see MAJOR-2.

**References**:
- [GreptimeDB issue #7670 — Leader Election Stall: PostgreSQL Advisory Locks Leak on Task Cancellation — accessed 2026-05-27](https://github.com/GreptimeTeam/greptimedb/issues/7670): confirms the cached-conn anti-pattern is a real, recently-reported production problem. Meta-review verified this citation as accurate.
- [Kerkour — Leader election with PostgreSQL's advisory locks — accessed 2026-05-27](https://kerkour.com/postgresql-leader-election-advisory-lock): URL exists; the PgBouncer-transaction-pool warning attributed verbatim in round-1 could not be confirmed verbatim by round-2 verification. The PgBouncer + advisory-lock incompatibility is independently true as an engineering fact (session-scoped locks released on transaction return when pool mode is `transaction`); rely on the engineering fact, not the quote.
- *(Stormatics split-brain HA Postgres quote removed: round-2 meta-review verified the article does not mention advisory locks at all; the verbatim quote was a fabricated paraphrase. See `final-decisions.md` § X1 for the drop verdict.)*

### [MAJOR-2 → NIT-deferred pending Phase 4] Outbox `ListPending` has no row-level claim; relies entirely on the advisory lock

> **Severity downgraded by round-2 meta-review + final-decisions D5**: same Phase-4 gating as MAJOR-1. Without a second relay replica (current deployment is single-pod), the advisory-lock-only serialisation is operationally sufficient. Re-promote when HA scale-out introduces a second relay. The recommendation remains the correct shape when that happens. See [`final-decisions.md` § D5](final-decisions.md).

**Where**: [`internal/application/outbox/relay.go:117-153`](../../internal/application/outbox/relay.go) calling `r.outboxRepo.ListPending(ctx, r.batchSize)`

**What**: The relay polls `events_outbox WHERE processed_at IS NULL` (partial index from migration 000007) and processes rows serially without `FOR UPDATE SKIP LOCKED`. The pg_try_advisory_lock(1001) is the SOLE serialisation mechanism. Combined with MAJOR-1, this means a split-brain scenario can re-publish every pending row twice (until one relay's `MarkProcessed` sticks).

**Why it matters**: Industry-standard outbox implementations (Debezium, Eventuate, AWS Prescriptive Guidance, the 2025 medium write-ups) all use `FOR UPDATE SKIP LOCKED` per-row as the primary serialisation, with leader election only as an optimisation. The pattern is "claim the row in the read, process, mark done" — atomic at the row level rather than at the relay level. The current design inverts this.

**Recommendation**: Change `ListPending` to issue `SELECT ... FROM events_outbox WHERE processed_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED` inside a transaction; keep MarkProcessed inside the same tx (UPDATE → COMMIT). This gives true row-level claim semantics, makes the advisory lock optional (it becomes a throughput optimisation: keep the single-active-relay model to avoid wasted PG round-trips, but the row claim is now safe even when the lock isn't held exclusively). Effort: M (one SQL change + one transaction-boundary refactor).

**References**:
- [Software Craftsperson — Transactional Outbox Pattern: A Practical Guide to Trade-offs — 2025-10-08](https://www.softwarecraftsperson.com/posts/2025-10-08-transactional-outbox-pattern/): the article describes `FOR UPDATE SKIP LOCKED` as the row-claim primitive used to safely scale outbox relays. Meta-review verified the substantive claim; verbatim quotation removed in favour of paraphrase.
- [Microservices.io — Transactional outbox pattern — accessed 2026-05-27](https://microservices.io/patterns/data/transactional-outbox.html)
- *(AWS Prescriptive Guidance "lists SKIP LOCKED as the primary primitive" claim removed: round-2 meta-review verified the AWS page's sample code uses Spring JPA `findAllByOrderByIdAsc + deleteAllInBatch` with NO row-level locking; the round-1 attribution was a fabricated paraphrase. See `final-decisions.md` § X1.)*

### [MAJOR-3] `payment_failed` webhook triggers immediate saga compensation; no in-window retry

> **Resolution path per `final-decisions.md` § D3**: the code at `webhook_service.go:482-485` carries a verbatim comment *"NO reservation-window guard here — failure is failure, the saga has to run regardless of TTL"* — this is an EXPLICIT counter-decision, not an oversight. The correct action is **open ADR-0002 (`docs/adr/0002_payment_failed_compensation_semantics.md`)** to either ratify the current behaviour or reverse it after weighing the in-window-retry scenarios, NOT a unilateral "two-line fix". The technical recommendation below remains correct ONLY if the ADR concludes the retry-window behaviour is preferred. See [`final-decisions.md` § D3](final-decisions.md) for the deferred-deliberate classification.

**Where**: [`internal/application/payment/webhook_service.go:482-538`](../../internal/application/payment/webhook_service.go) (`handleFailure`)

**What**: A `payment_intent.payment_failed` event from Stripe immediately transitions the order to `payment_failed` and emits `order.failed` to the outbox → saga compensator reverts Redis inventory. The reservation window (`reserved_until`, default 15m) is not honoured for retry **specifically in `handleFailure`**; the `reserved_until` field IS checked on the `/pay` intent-creation endpoint at `webhook_service.go:324` (`if !order.ReservedUntil().After(s.now())`), so the system is aware of the window — it just doesn't use it on the failure-webhook path.

**Why it matters**: The mainstream payment flow is `payment_intent.payment_failed` → client retries (most cards 3-D-Secure-redirect, fix CVV, etc.) within the reservation window → `payment_intent.succeeded`. The current design has no path for this — the moment the first webhook lands, the order is dead. Industry SOP (Stripe Payment Intents lifecycle docs, Adyen, Worldpay) is: keep the order in `awaiting_payment` for the reservation window; only declare `payment_failed` if `reserved_until` passes AND the most-recent attempt was failed. The D6 expiry sweeper already handles the timeout side; the missing piece is "don't auto-fail on first webhook".

Also caught in user memory file at `payment_retry_design_gap.md` (referenced in the session memory) — the user is aware of the gap. This finding is verification that it's still present.

**Recommendation**: Two-line fix in `handleFailure` — gate the MarkPaymentFailed + outbox emit on `time.Now().After(order.ReservedUntil())`. Before the window expires, log + 200 (don't escalate to saga); rely on the D6 sweeper to escalate to `expired` after the window. After the window, current behaviour holds. Effort: S, but warrants a test sweep for the cross-event ordering corner cases (success-after-fail, multi-fail, fail-then-D6).

**References**:
- [Simplico — Idempotency in Payment APIs: Prevent Double Charges with Stripe, Omise, and 2C2P — 2026-04-04](https://simplico.net/2026/04/04/idempotency-in-payment-apis-prevent-double-charges-with-stripe-omise-and-2c2p/): walks the retry-then-succeed flow as the expected case for declined-but-recoverable cards.
- [Stripe API — Idempotent requests — accessed 2026-05-27](https://docs.stripe.com/api/idempotent_requests): notes that webhooks for the same intent CAN deliver out of order under retry.
- User memory note `payment_retry_design_gap.md` (project-internal).

### [MAJOR-4] Booking-flow status non-monotonicity under tx rollback + PEL retry (latent, future bite)

**Where**: [`internal/application/worker/message_processor.go:84-133`](../../internal/application/worker/message_processor.go) interacting with `cache/redis_queue.go:processWithRetry` and the eventual handleFailure path

**What**: The Lua deduct commits to Redis BEFORE the worker's PG tx commits the matching `orders` row. If the PG tx rolls back (any reason — connection drop, deadlock, ctx cancel), Redis-side inventory stays deducted until either (a) the worker's retry within max_retries succeeds, or (b) max retries exhaust and `handleFailure` calls `RevertInventory`. There's a window (up to ~600ms default, longer with custom config) where Redis inventory is "deducted but no DB row exists". A reader of Redis (or any code that derives "what's available" from Redis) sees pessimistic over-deduction during that window.

**Why it matters**: Today the read path doesn't expose Redis inventory directly to clients — `available_tickets` is derived from PG via `ticket_types.available_tickets`. So the over-deduction is invisible to end-users. It DOES surface to operators via the inventory drift metric (`inventory_drift_orders` gauge) and the drift reconciler designed in `architectural_backlog.md` PR-D. The latent bite: any future feature that reads `event:{id}:qty` for "is it sold out?" decisions (the planned hot-section quota router from the 2026-05-03 sharding entry — Layer 1 routing) inherits this window without a guard.

**Recommendation**:
1. Document the window in `BookingService.BookTicket` doc — it's currently implicit and load-bearing for the read-path correctness.
2. Define a "Redis inventory is best-effort live; PG ticket_types is the SoT for sold-out decisions" contract, and audit future planned features (hot-section router, drift reconciler) against it.
3. Consider exposing the worker's retry-budget as a histogram so the actual P99 window length is observable (today the retry attempts are counted but the time-to-revert from worker failure is not).

**References**:
- [Shopify Engineering — We replaced Redis with MySQL for inventory reservations — 2026](https://shopify.engineering/scaling-inventory-reservations): "Shopify's oversell protection system handles inventory by reserving inventory during payment processing—a short hold that prevents two concurrent checkouts from claiming the same unit … For years, this ran on Redis. … Shopify ran both systems in parallel in 'shadow mode'". Worth reading as a worked example of the dual-tier durability story and the operational reasons to consider moving back toward DB-as-primary.

### [MINOR-1] Idempotency 3-op race window (Get → handler → Set non-atomic with Lua deduct)

**Where**: [`internal/infrastructure/api/middleware/idempotency.go:178-318`](../../internal/infrastructure/api/middleware/idempotency.go)

**What**: Two concurrent requests with the same `Idempotency-Key` and same body can both miss the cache, both run the handler (both deduct Redis inventory), both write a cached response (last write wins). The Idempotency-Key contract guarantees "one logical operation per key" only after the second key-bearing replay; during the race, the seller sees 2 inventory deducts.

**Why it matters**: Tracked + accepted in `docs/architectural_backlog.md` (2026-05-12 entry). The mitigation (server-minted UUIDv7 + DB UNIQUE) bounds the impact to Redis drift, not double-booked orders. At current scale this is the right trade-off. Documented honestly in `Phase 2 #4` interview defence.

**Recommendation**: Hold. The Stripe-style sentinel-SETNX pattern would close this but adds per-endpoint Lua surface and a separate sentinel-TTL failure mode. Cost > benefit at portfolio scale; the existing tracking memo is adequate.

**References**: As above, plus [Stripe blog — Designing robust and predictable APIs with idempotency — accessed 2026-05-27](https://stripe.com/blog/idempotency).

### [MINOR-2] `OrderQueue.Subscribe` interface doc lacks the at-least-once invariant statement

**Where**: [`internal/application/worker/queue.go:99-105`](../../internal/application/worker/queue.go)

**What**: The interface doc says "the queue manages retry / ACK / DLQ based on what handler returns" but doesn't state "delivery is at-least-once; handlers MUST be idempotent". The implementation is correctly at-least-once (PEL + idempotent revert.lua + DB UNIQUE), but a future maintainer building a new consumer against this interface might believe stronger guarantees.

**Recommendation**: One-line addition to the doc: "Delivery is at-least-once: handler may be invoked more than once for the same logical message during PEL recovery or transient handler failure. Handlers MUST be idempotent." Effort: XS.

### [MINOR-3] `revert.lua` idempotency key is `saga:reverted:{compensationID}` but worker-side parse-fail compensation uses `msgID` for the same key — semantic drift waiting to bite

**Where**: [`internal/infrastructure/cache/redis_queue.go:443`](../../internal/infrastructure/cache/redis_queue.go) (handleParseFailure) and [`internal/application/saga/compensator.go:163`](../../internal/application/saga/compensator.go) (HandleOrderFailed)

**What**: Two different callers of `RevertInventory` use two different semantic key spaces:
- Worker handleFailure / handleParseFailure: `compensationID = rawMsg.ID` (Redis stream id `<ts>-<seq>`)
- Saga compensator: `compensationID = "order:" + orderID`

Both end up at `revert.lua` as `KEYS[2] = saga:reverted:{compensationID}`. There's no collision today because the stream id has a `-` and the saga path prefixes `order:` — so the key namespaces don't intersect. But the abstraction is a single "revert key" with two unrelated semantics, and a future change that consolidates to "use orderID as the compensation key" would silently lose idempotency for any in-flight PEL retry.

**Recommendation**: Either (a) document the dual-namespace explicitly in `revert.lua` and a comment in `domain.InventoryRepository.RevertInventory`, or (b) prefix both callers with their own namespace (`worker:msg:<id>` vs `saga:order:<id>`) so the boundary is explicit. Effort: XS for docs only, S for the rename + migration.

### [NIT-1] Doc: outbox relay leader-election claim should soften with conn-loss caveat

**Where**: [`internal/application/outbox/relay.go:14-20`](../../internal/application/outbox/relay.go)

**What**: Comment claims leader election "across all Relay replicas" — true when the advisory-lock cache is sound (MAJOR-1). Soften to acknowledge the bounded split-brain window if the cached-conn fix isn't taken.

### [NIT-2] `redis_runtime_metadata_scaling.md` would benefit from a worked Shopify cross-reference

**Where**: [`docs/design/redis_runtime_metadata_scaling.md`](../design/redis_runtime_metadata_scaling.md)

**What**: The doc is already excellent. Adding a one-paragraph cross-reference to Shopify's [2026 MySQL-for-reservations write-up](https://shopify.engineering/scaling-inventory-reservations) strengthens the "why Redis here" defence — Shopify moved the OTHER direction and the rationale is operational, not correctness.

---

## Recommendations summary

| Finding | Action | Effort |
| --- | --- | --- |
| MAJOR-1 | Add `SELECT 1` ping inside `pgAdvisoryLock.TryLock` cached-conn path, OR drop the cache | S |
| MAJOR-2 | Add `FOR UPDATE SKIP LOCKED` to outbox `ListPending`; keep MarkProcessed in the same tx | M |
| MAJOR-3 | Gate `handleFailure` on `Now().After(reserved_until)`; rely on D6 sweeper otherwise | S (+ test sweep) |
| MAJOR-4 | Document the deduct-to-PG-tx window in `BookingService.BookTicket`; add a worker-failure histogram | S (docs) / M (metric) |
| MINOR-1 | Hold — tracking memo adequate | — |
| MINOR-2 | One-line at-least-once invariant added to `OrderQueue.Subscribe` interface doc | XS |
| MINOR-3 | Document the dual-namespace of `revert.lua` compensation key, OR prefix both callers | XS / S |
| NIT-1 | Soften `outbox/relay.go:14-20` leader-election claim | XS |
| NIT-2 | Add Shopify cross-reference to `redis_runtime_metadata_scaling.md` | XS |

---

## References

- [AWS Prescriptive Guidance — Transactional outbox pattern — accessed 2026-05-27](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html)
- [Software Craftsperson — Transactional Outbox Pattern: A Practical Guide to Trade-offs — 2025-10-08](https://www.softwarecraftsperson.com/posts/2025-10-08-transactional-outbox-pattern/)
- [Microservices.io — Pattern: Transactional outbox — accessed 2026-05-27](https://microservices.io/patterns/data/transactional-outbox.html)
- [NP Blog — Transactional Outbox Pattern: From Theory to Production — 2025-05-19](https://www.npiontko.pro/2025/05/19/outbox-pattern)
- [Outbox Pattern in 2025: Still Relevant or Just Legacy Bloat? — Medium / techWithNeer — 2025](https://medium.com/@neerupujari5/outbox-pattern-in-2025-still-relevant-or-just-legacy-bloat-fa00e5d13612)
- [Kerkour — Leader election with PostgreSQL's advisory locks — accessed 2026-05-27](https://kerkour.com/postgresql-leader-election-advisory-lock)
- [Stormatics — Understanding Split-Brain Scenarios in Highly Available PostgreSQL Clusters — accessed 2026-05-27](https://stormatics.tech/blogs/understanding-split-brain-scenarios-in-highly-available-postgresql-clusters)
- [GreptimeTeam/greptimedb#7670 — Leader Election Stall: PostgreSQL Advisory Locks Leak on Task Cancellation — accessed 2026-05-27](https://github.com/GreptimeTeam/greptimedb/issues/7670)
- [Yamishift / Medium — Node.js + Redis Streams: Exactly-Once Event Pipelines with Consumer Groups — 2025](https://medium.com/@komalbaparmar007/node-js-redis-streams-exactly-once-event-pipelines-with-consumer-groups-33318dfbd591)
- [Redis docs — Streams (XREADGROUP, consumer groups, PEL) — accessed 2026-05-27](https://redis.io/docs/latest/develop/data-types/streams/)
- [Stripe docs — Idempotent requests — accessed 2026-05-27](https://docs.stripe.com/api/idempotent_requests)
- [Stripe blog — Designing robust and predictable APIs with idempotency — accessed 2026-05-27](https://stripe.com/blog/idempotency)
- [Simplico — Idempotency in Payment APIs: Prevent Double Charges with Stripe, Omise, and 2C2P — 2026-04-04](https://simplico.net/2026/04/04/idempotency-in-payment-apis-prevent-double-charges-with-stripe-omise-and-2c2p/)
- [Shopify Engineering — We replaced Redis with MySQL for inventory reservations—and it scaled — 2026](https://shopify.engineering/scaling-inventory-reservations)
- [InfoQ — Shopify's Architecture to Handle the World's Biggest Flash Sales — accessed 2026-05-27](https://www.infoq.com/presentations/shopify-architecture-flash-sale/)
- [Medium — The Saga Pattern Race Conditions — 2025-12](https://medium.com/@har.avetisyan2002/the-saga-pattern-race-conditions-2ba9e59a28c2)
- [TheCodeForge — Saga Pattern Explained: Distributed Transactions, Compensations & Production Pitfalls — accessed 2026-05-27](https://thecodeforge.io/system-design/saga-pattern/)
- [Temporal — Saga Pattern in Microservices: A Mastery Guide — accessed 2026-05-27](https://temporal.io/blog/mastering-saga-patterns-for-distributed-transactions-in-microservices)
- [Apache Seata — TCC mode documentation — accessed 2026-05-27](https://seata.apache.org) — referenced for the fence-pattern shape used in the saga compensator's PG `order_compensations` + Redis EXISTS-guard combination.
- [InfoQ — Jepsen: Testing the Partition Tolerance of PostgreSQL, Redis, MongoDB and Riak — historical reference](https://www.infoq.com/articles/jepsen/) — historical reference for the well-known "Redis with failover is best-effort cache" framing. Cited because it shaped the cache-truth architecture decision in `docs/architectural_backlog.md` 2026-05-03. No 2024 Jepsen Redis follow-up surfaced in research.
