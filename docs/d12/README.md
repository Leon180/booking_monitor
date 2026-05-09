# D12 — 4-stage architecture comparison harness

> Status: **D12 closed — PR-D12.1 (Stage 1) + PR-D12.2 (Stage 2) + PR-D12.3 (Stage 3) + PR-D12.4 (Stage 4 + observability) + PR-D12.5 (multi-target harness + comparison.md) all shipped.** See [docs/benchmarks/comparisons/](../benchmarks/comparisons/) for the apples-to-apples comparison reports.

D12 is the senior-portfolio centerpiece of Phase 3 (per [`docs/post_phase2_roadmap.md`](../post_phase2_roadmap.md) L104-L111). Four separate Go binaries share the same `internal/` packages but use different fx wirings and different `booking.Service` implementations — running the same workload across all four under [`scripts/k6_two_step_flow.js`](../../scripts/k6_two_step_flow.js) produces a side-by-side benchmark table that quantifies each architectural decision's cost vs. headroom.

## The 4 stages

| Stage | `cmd/` binary | Architecture | What it adds vs. previous |
|---|---|---|---|
| **1** | `cmd/booking-cli-stage1/` | API → Postgres `BEGIN; SELECT FOR UPDATE; UPDATE event_ticket_types; INSERT orders; COMMIT;` | (baseline) |
| **2** | `cmd/booking-cli-stage2/` | API → Redis Lua atomic deduct → SYNCHRONOUS PG INSERT; revert.lua on INSERT failure | Redis hot-path inventory (SoT migrates to `ticket_type_qty:{id}`); no async buffering — saturation IS the sync PG INSERT |
| **3** | `cmd/booking-cli-stage3/` | API → Redis Lua → `orders:stream` → async worker → DB INSERT (worker UoW: `DecrementTicket` + `Order.Create`); revert.lua + UPDATE on compensator path | async via stream + worker buffers the PG INSERT off the request hot path; no Kafka outbox / saga consumer (in-binary expiry sweeper handles abandon) |
| **4** | `cmd/booking-cli/` (current) | API → Redis Lua → `orders:stream` → worker → DB INSERT; **money movement via** `/pay` + D5 webhook (not Kafka-driven post-D7); `order.failed` outbox → Kafka → in-process saga compensator (D5 webhook `payment_failed` + D6 expiry sweeper + recon force-fail as the only producers) | full Pattern A end-to-end with saga compensation only on the failure path; PR-D12.4 adds throughput observability (3 metrics + 4 alerts + outcome-label exhaustiveness) |

> **Stage 4 ≠ the README's "Architecture Evolution" Stage 4 diagram.** The README's evolution section labels its Stage 4 as the *historical* `v0.2.0–v0.4.0` pre-D7 architecture (`payment_worker` + `order.created` Kafka topic). D12 Stage 4 is the **current post-D7 binary** — `payment_worker` removed, saga consumer in-process inside `app`. `comparison.md` (lands in PR-D12.5) calls this out explicitly so readers don't conflate the two.

## Apples-to-apples contract

The key research-informed framing: **all four stages preserve the same two-step API contract** so [`scripts/k6_two_step_flow.js`](../../scripts/k6_two_step_flow.js) (already shipped in [PR #102](https://github.com/Leon180/booking_monitor/pull/102)) runs **unmodified** against any stage's binary.

```
POST /api/v1/book              → 202 Accepted with {order_id, status:"reserved",
                                                     reserved_until, links.{self,pay}}
POST /api/v1/orders/:id/pay    → 200 OK with {order_id, payment_intent_id,
                                              client_secret, amount_cents, currency}
POST /test/payment/confirm/:id?outcome=succeeded|failed → 200 OK
GET /api/v1/orders/:id         → 200 OK with {status, ...}
POST /api/v1/events            → 201 Created (k6 setup; per-run event creation)
GET /livez                     → 200 OK
GET /metrics                   → Prometheus default registry
```

Same shape, same status codes, same error-sentinel mappings (`ErrSoldOut → 409`, `ErrUserAlreadyBought → 409`, `ErrTicketTypeNotFound → 404`, `ErrInvalid* → 400`). What differs across stages is the *internal* implementation; the wire shape is invariant.

This is what makes the comparison apples-to-apples. Stages 1+2 could in principle return `201 Created` with the row already committed (since they're synchronous), but they return `202 Accepted` to match Stages 3+4 — preserving the contract is more valuable than reflecting internal sync vs. async.

---

## Stage 1 — synchronous SELECT FOR UPDATE baseline (PR-D12.1)

### Architecture

```
POST /book
  ┌─────────────────────────────────────────────────────────┐
  │ BEGIN                                                   │
  │   SELECT event_id, available_tickets, price_cents,      │
  │          currency                                       │
  │     FROM event_ticket_types WHERE id = $1 FOR UPDATE    │
  │   if available < quantity → ROLLBACK + ErrSoldOut       │
  │   UPDATE event_ticket_types                             │
  │      SET available_tickets -= $qty, version += 1        │
  │   INSERT orders (..., status='awaiting_payment',        │
  │                  reserved_until = NOW() + window,       │
  │                  amount_cents + currency snapshot)      │
  │ COMMIT                                                  │
  └─────────────────────────────────────────────────────────┘

POST /pay
  read order; eligibility-guard status + reserved_until > NOW()
  generate fake `pi_stage1_<uuid7>`; UPDATE orders
    SET payment_intent_id=$1
    WHERE id=$2 AND payment_intent_id IS NULL
              AND status='awaiting_payment'
              AND reserved_until > NOW()        ← atomic TTL guard
  on RowsAffected=0: re-read + disambiguate (concurrent /pay won, status changed, expired)

POST /test/payment/confirm/:id?outcome=succeeded
  UPDATE orders SET status='paid'
    WHERE id=$1 AND status='awaiting_payment'
              AND payment_intent_id IS NOT NULL  ← /pay-first contract
              AND reserved_until > NOW()         ← atomic TTL guard

POST /test/payment/confirm/:id?outcome=failed
  pre-check payment_intent_id IS NOT NULL  (matches Stage 4's mock-confirm contract)
  call compensateAwaitingOrder  (shared helper)

In-binary expiry sweeper goroutine (every cfg.Expiry.SweepInterval)
  SELECT id FROM orders
    WHERE status='awaiting_payment'
      AND reserved_until <= NOW() - $grace::interval LIMIT 100
  for each id: compensateAwaitingOrder(ctx, db, id)

compensateAwaitingOrder helper (shared between /confirm-failed AND sweeper)
  BEGIN
    SELECT ticket_type_id, quantity, status FROM orders WHERE id=$1 FOR UPDATE
    if status != 'awaiting_payment' → ErrCompensateNotEligible (rolled back)
    UPDATE event_ticket_types SET available_tickets += qty
    UPDATE orders SET status='compensated' WHERE status='awaiting_payment'
  COMMIT
```

### What Stage 1 deliberately omits

No Redis client, no Kafka client, no async worker, no outbox relay, no out-of-process saga compensator (replaced by the in-binary sweeper above), no payment gateway (the `/pay` returns a stub `client_secret`), no recon, no saga watchdog, no drift detector, no idempotency middleware (Stage 4 has it; Stages 1-3 opt out so the comparison surfaces the synchronous baseline cost without N4 fingerprinting overhead).

### Inventory source-of-truth

`event_ticket_types.available_tickets` is the **only** inventory column Stage 1 reads or writes. The legacy `events.available_tickets` was frozen post-D4.1 (initialised at create, never written thereafter). Using it would make Stage 1 fast but **not comparable** to Stages 2-4. The integration test's `seedTicketType` helper deliberately seeds `events.available_tickets` to `ticket_type_stock × 2` — divergent — so any regression that read from the wrong column would surface as conservation-check failure.

### How to run Stage 1

```bash
# Build
go build -o bin/booking-cli-stage1 ./cmd/booking-cli-stage1

# Run (requires Postgres reachable via DATABASE_URL)
DATABASE_URL='postgres://booking:smoketest_pg_local@localhost:5433/booking?sslmode=disable' \
  PORT=8091 \
  BOOKING_RESERVATION_WINDOW=20s \
  EXPIRY_SWEEP_INTERVAL=5s \
  EXPIRY_GRACE_PERIOD=1s \
  bin/booking-cli-stage1 server
```

The env vars match Stage 4's so `make demo-up`-style overrides propagate without per-stage adaptation. PR-D12.5 will introduce per-stage Postgres database isolation (`booking_stage1`, `booking_stage2`, …) so all four binaries can run side-by-side without cross-talk.

### How Stage 1 was verified

| Verification | What it pins |
|---|---|
| **Service-level integration tests** ([`test/integration/postgres/sync_booking_test.go`](../../test/integration/postgres/sync_booking_test.go)) — 5 tests, ~6s total | HappyPath / SoldOut / TicketTypeNotFound / **ConcurrentContention** (20 goroutines × stock=5 — pins the row-lock serialization) / **DuplicateActiveOrder** (asserts both `domain.ErrUserAlreadyBought` sentinel AND inventory rollback to 4, not the doomed mid-tx 3) |
| **Live HTTP smoke** | `make demo-up`-style stack + Stage 1 binary on `:8091`. All 3 k6 paths exercised: happy → paid; payment-failed → compensated + inventory restored; abandon (no /pay) → TTL → sweeper → compensated + inventory restored |
| **Graceful shutdown** | SIGTERM → fx OnStop chain → sweeper drains + bounded by stopCtx → HTTP server shutdown. End-to-end ~6ms when sweeper is between ticks |
| **fx.Shutdowner escalation on listener failure** | Second instance on same port → `bind: address already in use` → `fx.Shutdown(ExitCode(1))` → process exits non-zero in ~3s. First instance unaffected |

### Architectural cost Stage 1 surfaces

Per the [research-informed framing in the D12 plan](../post_phase2_roadmap.md), Stage 1's `SELECT FOR UPDATE` plateau is the **physical baseline** the comparison harness measures against:

- **Postgres 16-18 row-lock contention ceiling**: ~1-2k TPS even on strong hardware ([AWS Aurora 2024](https://aws.amazon.com/blogs/database/improve-postgresql-performance-diagnose-and-mitigate-lock-manager-contention/), [PostgresAI 2025](https://postgres.ai/blog/20251009-postgres-marathon-2-005)). Goal is to **demonstrate** this ceiling, not exceed it.
- **Concurrent-contention serialization**: every booking on the same `event_ticket_types` row blocks behind the prior tx's COMMIT. The integration test pins this with 20 concurrent goroutines on stock=5 → exactly 5 succeed, 15 ErrSoldOut, 0 other errors.

PR-D12.5's `comparison.md` will frame the Stages 2/3/4 results as **headroom under contention that Stage 1 physically can't reach** — NOT as "Stage 4 is faster everywhere" (no 2024-2026 paper supports the latter framing per the academic-researcher pass).

---

## Stage 2 — Redis Lua atomic deduct + sync PG INSERT (PR-D12.2)

### Architecture

```
POST /book
  ┌─────────────────────────────────────────────────────────┐
  │ EVAL deduct_sync.lua                                    │
  │   KEYS: ticket_type_qty:{id}, ticket_type_meta:{id}     │
  │   ARGV: count                                           │
  │   1. DECRBY ticket_type_qty:{id} count                  │
  │   2. if new < 0: INCRBY (restore) → return "sold_out"   │
  │   3. HMGET ticket_type_meta:{id} event_id price_cents   │
  │      currency                                           │
  │   4. if any field missing: INCRBY (restore)             │
  │      → return "metadata_missing" (cold-fill repair      │
  │         path on Go side; retries once)                  │
  │   5. amount_cents = price_cents × count (decimal-string │
  │      multiply; same precision-safe code as deduct.lua)  │
  │   6. return "ok", event_id, amount_cents, currency      │
  └─────────────────────────────────────────────────────────┘
  ↓ on "ok":
  INSERT INTO orders (id, user_id, event_id, ticket_type_id,
                      quantity, status='awaiting_payment',
                      amount_cents, currency, reserved_until,
                      created_at) VALUES (...)
  ↓ on 23505 (uq_orders_user_event tripped) OR generic INSERT failure:
  EVAL revert.lua (idempotent via saga:reverted:order:<id> SETNX)
  return ErrUserAlreadyBought (or wrapped error)

POST /pay              ← stagehttp.HandlePayIntent (unchanged from Stage 1)
                          PaymentIntent prefix: `pi_stage2_`
POST /test/payment/confirm/:id   ← stagehttp.HandleTestConfirm (unchanged)

In-binary expiry sweeper goroutine — same shape as Stage 1; the
only difference is the injected Compensator now adds the revert.lua
step before the SQL UPDATEs.

stage2Compensator (shared by sweeper + /confirm-failed)
  BEGIN
    SELECT ticket_type_id, quantity, status FROM orders WHERE id=$1 FOR UPDATE
    if status != 'awaiting_payment' → ErrCompensateNotEligible (rolled back)
    EVAL revert.lua against ticket_type_qty:{ttID}      ← Stage 2 ONLY
       (idempotent via saga:reverted:order:<id> SETNX)
    UPDATE orders SET status='compensated' WHERE status='awaiting_payment'
  COMMIT
  -- NO UPDATE event_ticket_types here. Symmetric with the
  -- forward path which doesn't decrement that column either.
  -- An asymmetric compensator would inflate the PG column on
  -- every abandon (Codex round-1 P1).

POST /events
  PG tx: INSERT events + INSERT event_ticket_types + COMMIT
  ↓ on PG commit success:
  inventoryRepo.SetTicketTypeRuntime(ctx, tt)
     HSET ticket_type_meta:{id} event_id price_cents currency
     SET ticket_type_qty:{id} total_tickets EX 24h
  ↓ on Redis hydrate failure:
  compensateDanglingEvent(detached_ctx, db, eventID, ttID)
     DELETE event_ticket_types + DELETE events  (FK-safe order)
  return 500 to client. The caller can retry /events cleanly
  because the PG rows are gone. This is fail-and-compensate
  (Codex round-1 P2; mirrors event.service's pattern at
  internal/application/event/service.go:165-199) — best-effort
  hydrate would leave the event permanently un-bookable because
  deduct_sync.lua's DECRBY-against-missing-key path returns
  sold_out before the metadata_missing repair runs.
```

### What Stage 2 adds vs Stage 1

| Layer | Stage 1 | Stage 2 |
|---|---|---|
| Hot-path serialization | Postgres row lock on `event_ticket_types` | Redis Lua single-thread execution on `ticket_type_qty:{id}` |
| Inventory SoT | `event_ticket_types.available_tickets` | `ticket_type_qty:{id}` (Redis); PG column unchanged on hot path |
| Metadata read | Inline in the SELECT FOR UPDATE | HMGET `ticket_type_meta:{id}` inside Lua |
| Compensation | SQL UPDATE event_ticket_types += qty + UPDATE orders | revert.lua + UPDATE orders only (PG inventory column untouched on hot path AND on compensation; symmetric) |
| Failure-mode for INSERT 23505 | tx rollback restores PG inventory | revert.lua restores Redis qty (SETNX-guarded for idempotency) |
| `/events` | PG only | PG + Redis hydrate; Redis failure → PG compensation (DELETE both rows) → 500 |

Everything else is identical. **The single architectural change is the inventory SoT migration.** Stage 2's whole comparison-harness contribution is isolating the cost-vs-benefit of moving inventory off the PG row lock onto Redis Lua atomicity.

### Why a separate `deduct_sync.lua` (not reuse of `deduct.lua`)

Stage 4's `deduct.lua` ends with `XADD orders:stream` to enqueue the async worker. Stage 2 has no worker, so reusing `deduct.lua` would silently accumulate orphan stream messages (the harness's PR-D12.5 per-stage Redis DB index ultimately mitigates this, but it's the wrong layer). The new `deduct_sync.lua` is `deduct.lua` minus that XADD block — the success-branch wire shape (`{"ok", event_id, amount_cents, currency}`) is byte-identical so the Go-side parser is shared between `RedisSyncDeducter.Deduct` and `redisInventoryRepository.DeductInventory`. The decimal-string multiply for `amount_cents` precision is duplicated verbatim across the two scripts; Redis Lua has no include mechanism, so a future bugfix lands in both. The plan's slice-1 review note flags this duplication as accepted.

### Inventory source-of-truth (Stage 2's most load-bearing design rule)

`ticket_type_qty:{id}` in **Redis** is authoritative on the booking hot path. `event_ticket_types.available_tickets` (the column Stage 1 uses) is **untouched** during Stage 2's `BookTicket` AND during Stage 2's compensation — symmetric. PG admin / drift-detector readers see the originally-seeded `total_tickets` value forever; Redis qty is the live count. The Stage 2 integration tests pin BOTH directions: `TestSyncLuaBooking_HappyPath` asserts PG column unchanged after a successful book (Redis qty −1); `TestSyncLuaBooking_AbandonCompensator` asserts PG column unchanged after compensation (Redis qty +1). A regression in either direction — a stray UPDATE on the forward path OR an asymmetric increment in the compensator — would trip a conservation-check assertion. (The asymmetric-compensator bug shipped in slice 4's first cut; Codex round-1 P1 caught it before merge.)

The Stage 1 → Stage 2 SoT migration matters for the comparison harness because it's the actual *change* being measured. Stage 1's bottleneck is row-lock contention; Stage 2's bottleneck is the synchronous PG INSERT (Redis Lua doesn't bottleneck at 500-1000 TPS — see Stage 2's saturation framing below).

### Lua failure-revert ordering (the critical correctness invariant)

The plan §risks #2 + #3 cases — "INSERT-failure-revert ordering" and "uq_orders_user_event must trigger revert.lua" — are pinned by `TestSyncLuaBooking_DuplicateActiveOrderRevertsRedis`. Without revert.lua firing on 23505, every duplicate-active-order would silently consume Redis inventory (PG knows nothing because the INSERT rolled back, but Redis already DECRBY'd). The test seeds `qty=5`, books once successfully (`qty=4`), then books a duplicate by the same user → `ErrUserAlreadyBought` AND `qty` restored to `4`. revert.lua's `saga:reverted:order:<id>` SETNX guard makes this revert path idempotent with any later saga-side compensation that fires on the same orderID.

The compensator's revert.lua call sits **inside** the same SQL transaction that does the FOR UPDATE + status check + UPDATE statements. Lua-fail rolls back the whole tx; the next sweep retries, and the SETNX guard short-circuits the second Lua call so the revert isn't double-applied. The saga `revert.lua` header comment captures the appendfsync rationale ("loud over-revert beats silent under-revert") that drives the EXISTS-not-NX guard.

### Why "atomic Redis deduct + sync DB write + Redis revert" has no canonical industry name

Per the plan's research-informed framing:

- **Closest analogs**: ProsperWorks' Ick (Redis 2PC-via-ZREM, since 2015) + Alibaba's snap-up flash-sale design. Neither matches Stage 2 exactly. Stage 2 is **folk pattern made explicit** — not a vendor-named architecture.
- **Theoretical framing**: Wang et al., *TXSQL — Lock Optimizations Towards High Contended Workloads* (SIGMOD '25, [DOI 10.1145/3722212.3724457](https://dl.acm.org/doi/10.1145/3722212.3724457); arXiv [2504.06854](https://arxiv.org/abs/2504.06854) extended). Tencent's industry paper proves hot-key contention can't be solved by better lock managers — they introduce "group locking" (serial-within-conflict-group, no per-row latches). Lua's single-thread execution is structurally analogous: bookings on the same `ticket_type_qty:{id}` serialize through the Redis event loop, but with no row-lock held across the network round-trip back to Go. Stage 2 isn't an optimization vs Stage 1; it's the architectural move the contention literature predicts.
- **Consistency level**: Bailis et al., *Highly Available Transactions: Virtues and Limitations* (PVLDB '14, [PDF](http://www.bailis.org/papers/hat-vldb2014.pdf)). Stage 2's "Redis succeeded, DB will succeed eventually" semantics live in HAT's Read Committed + Read-Your-Writes band. PR-D12.5's `comparison.md` will use Bailis HAT to answer the consistency-framing question explicitly across all four stages.

### Saturation point IS the sync PG INSERT (by design)

The plan's research-finding #4 is the headline: at 500+ TPS, network RTT + DB INSERT latency dominate Lua. **Stage 2's bottleneck is the synchronous Postgres INSERT**, not Lua atomicity. This is what the comparison harness needs Stage 2 to surface. PR-D12.5's `comparison.md` will frame Stages 3+4 as "headroom under contention that Stage 2 physically can't reach because the sync DB write serializes" — Stage 2's data point IS the architectural pivot in the comparison.

Stage 2 is intentionally NOT marketed as "Lua makes it fast." It's marketed as **"Lua + sync DB write trades async-buffering throughput for stronger consistency"** — the trade-off the multi-stage comparison exists to measure.

### How to run Stage 2

```bash
# Build
go build -o bin/booking-cli-stage2 ./cmd/booking-cli-stage2

# Run (requires both Postgres AND Redis)
DATABASE_URL='postgres://booking:smoketest_pg_local@localhost:5433/booking?sslmode=disable' \
  REDIS_PASSWORD=smoketest_redis_local \
  PORT=8092 \
  BOOKING_RESERVATION_WINDOW=20s \
  EXPIRY_SWEEP_INTERVAL=5s \
  EXPIRY_GRACE_PERIOD=1s \
  bin/booking-cli-stage2 server
```

Same env-var conventions as Stage 1; PR-D12.5 will introduce per-stage Postgres database (`booking_stage2`) AND per-stage Redis DB index (`db=1`) so Stages 1+2 can run side-by-side on the same Docker stack without cross-talk.

### How Stage 2 was verified

| Verification | What it pins |
|---|---|
| **Service-level unit tests** ([`internal/application/booking/synclua/service_test.go`](../../internal/application/booking/synclua/service_test.go)) — 11 tests | Validation paths (`ErrInvalidUserID` / `ErrInvalidQuantity` / `ErrInvalidOrderTicketTypeID`); sold-out branch; metadata-missing → repair → retry; repair-fail → wrapped error; pagination normalization; Deducter interface contract pinned via hand-rolled fake (miniredis kept out per plan §3 fidelity caveat) |
| **Service-level integration tests** ([`test/integration/postgres/synclua_booking_test.go`](../../test/integration/postgres/synclua_booking_test.go)) — 6 tests, ~9s total | HappyPath (Redis qty −1, PG column UNCHANGED) / SoldOut (Lua atomic INCRBY-restore) / **MetadataMissingRepairs** (full PG → Redis HSET round-trip; net qty decrement = 1, not 2) / **DuplicateActiveOrderRevertsRedis** (the 23505 → revert.lua case from the plan §risks) / **ConcurrentContention** (20 goroutines × stock=5; Lua single-thread serialization) / **AbandonCompensator** (revert.lua + UPDATE orders; PG `event_ticket_types.available_tickets` UNCHANGED — symmetry assertion catches the Codex round-1 P1 bug; SETNX idempotency guard armed; second Compensate is no-op) |
| **/events compensation tests** ([`cmd/booking-cli-stage2/compensate_test.go`](../../cmd/booking-cli-stage2/compensate_test.go)) — 2 tests | `compensateDanglingEvent` deletes both rows in FK-safe order + idempotent on missing rows (retry safety per the function's doc comment) |
| **Live HTTP smoke** | `make demo-up` stack + Stage 2 binary on `:8092`. All 3 k6 paths exercised: happy → paid; payment-failed → compensated + Redis qty restored (PG column unchanged — symmetric); abandon (no /pay) → TTL → sweeper → compensated + same restoration |
| **Graceful shutdown** | Same fx OnStop chain as Stage 1; sweeper goroutine drains bounded by stopCtx |
| **fx.Shutdowner escalation on listener failure** | Inherited verbatim from Stage 1's pattern; same `bind: address already in use` → `fx.Shutdown(ExitCode(1))` semantics |

### Architectural cost Stage 2 surfaces vs Stage 1

The integration test `TestSyncLuaBooking_ConcurrentContention` (20 goroutines × stock=5) returns the same shape Stage 1's contention test does: exactly 5 succeed, 15 ErrSoldOut. **The serialization point moved from Postgres row lock to Redis Lua single-threaded execution.** Throughput-wise, this matters because:

- The Lua call is one Redis network round-trip (~0.3-0.8 ms localhost).
- The PG INSERT is one Postgres round-trip + commit fsync (~3-8 ms localhost).
- In Stage 1 the row lock is held across the INSERT — concurrent bookings serialize behind the COMMIT.
- In Stage 2 the Lua serialization point releases as soon as DECRBY+HMGET return — concurrent bookings issue their PG INSERTs in parallel.

The cost: **Stage 2 carries strictly more failure modes than Stage 1.** The INSERT can fail AFTER Lua succeeded, requiring revert.lua. Redis can be down, Lua can return `metadata_missing` requiring a cold-fill repair, the SETNX idempotency guard can race. PR-D12.5's `comparison.md` will quantify the throughput delta vs the operational complexity delta — the comparison harness is the venue where the trade-off is honestly measured rather than asserted.

---

## Stage 3 — Redis Lua + orders:stream + async worker (PR-D12.3)

### Architecture

```
POST /book
  ┌─────────────────────────────────────────────────────────┐
  │ EVAL deduct.lua  (Stage 4's existing script — reused    │
  │                   verbatim, NOT a Stage-3-specific fork)│
  │   KEYS: ticket_type_qty:{id}, ticket_type_meta:{id}     │
  │   ARGV: count, user_id, order_id, reserved_until_unix,  │
  │         ticket_type_id                                  │
  │   1. DECRBY ticket_type_qty:{id} count                  │
  │   2. if new < 0: INCRBY (restore) → return "sold_out"   │
  │   3. HMGET ticket_type_meta:{id} (event_id, price, currency)
  │   4. if metadata missing: INCRBY → return "metadata_missing"
  │   5. amount_cents = price_cents × count                 │
  │   6. XADD orders:stream {message fields}                │
  │   7. return "ok", event_id, amount_cents, currency      │
  └─────────────────────────────────────────────────────────┘
  ↓ return 202 Accepted (no PG round-trip on hot path)

────────────  async worker goroutine  ────────────
  XReadGroup orders:stream → parseMessage
  ↓ on parse-fail: handleParseFailure (best-effort revert.lua + DLQ)
  ↓ on ok:
  uow.Do(ctx, func(repos) {
    repos.TicketType.DecrementTicket(ttID, qty)   // PG inventory −qty
    repos.Order.Create(order)                     // INSERT order row
  })
  ↓ on UoW error (23505 / transient): handleFailure
     (revert.lua INCRBY + DLQ; UoW rollback already restored PG)

POST /pay              ← stagehttp.HandlePayIntent (unchanged from Stages 1+2)
                          PaymentIntent prefix: `pi_stage3_`
POST /test/payment/confirm/:id   ← stagehttp.HandleTestConfirm (unchanged)

In-binary expiry sweeper goroutine — same shape as Stages 1+2;
only difference is the injected Compensator (now stage3Compensator).

stage3Compensator (shared by sweeper + /confirm-failed)
  BEGIN
    SELECT ticket_type_id, quantity, status FROM orders WHERE id=$1 FOR UPDATE
       (NullUUID scan; sql.ErrNoRows → ErrCompensateNotEligible)
       (status != 'awaiting_payment' → ErrCompensateNotEligible)
       (ttID NULL → ErrCompensateNotEligible — pre-D4.1 legacy guard)
    EVAL revert.lua against ticket_type_qty:{ttID}     ← Stage 2/3 ONLY
       (idempotent via saga:reverted:order:<id> SETNX)
    UPDATE event_ticket_types SET available_tickets += qty, version += 1
       (RowsAffected check: 0 rows → orphaned ticket_type, surface as
        hard error so the sweeper retries / ops investigates)
    UPDATE orders SET status='compensated' WHERE status='awaiting_payment'
  COMMIT

POST /events
  PG tx: INSERT events + INSERT event_ticket_types + COMMIT
  ↓ on PG commit success:
  inventoryRepo.SetTicketTypeRuntime(ctx, tt)
     HSET ticket_type_meta:{id} event_id price_cents currency
     SET ticket_type_qty:{id} total_tickets EX 24h
  ↓ on Redis hydrate failure:
  compensateDanglingEvent(detached_ctx, db, eventID, ttID)
     DELETE event_ticket_types + DELETE events  (FK-safe order)
  return 500 (same fail-and-compensate pattern as Stage 2;
  duplicated across stage 2 and stage 3 cmd binaries due to
  `package main` constraint).
```

### What Stage 3 adds vs Stage 2

| Layer | Stage 2 | Stage 3 |
|---|---|---|
| Hot-path serialization | Lua + sync PG INSERT (PG INSERT IS the bottleneck by design) | Lua + async PG INSERT (PG round-trip moves OFF the request path; saturation moves to Lua + worker throughput) |
| Service impl | `synclua.Service` (new in PR-D12.2) | Stage 4's existing `booking.Service` (reused verbatim) |
| Async worker | none | `internal/application/worker/` (Stage 4's existing, reused verbatim) |
| Lua script | `deduct_sync.lua` (Stage-2-specific fork, no XADD) | `deduct.lua` (Stage 4's existing, includes XADD) |
| PG inventory column on hot path | UNCHANGED (Stage 2 has no worker; INSERT orders only) | DECREMENTED inside worker UoW (`repos.TicketType.DecrementTicket`) |
| PG inventory column on compensation | UNCHANGED (symmetric) | INCREMENTED back (symmetric — the load-bearing rule) |
| Compensator | revert.lua + UPDATE orders only | revert.lua + UPDATE event_ticket_types += qty + UPDATE orders |
| Outbox / Kafka / saga | none | none (deferred to Stage 4) |

The architectural change is **adding the async worker**. The PG-inventory-column behavior MUST flip in lockstep: Stage 2's hot path doesn't decrement the column → compensator doesn't increment. Stage 3's worker UoW DOES decrement → compensator MUST increment. Asymmetric in either direction is a permanent inventory leak.

### Reuse strategy — the Stage 4 substrate

Stage 3's PR is small (~700 LOC vs Stage 2's ~2.5k) because almost everything below the cmd-binary layer is Stage 4's:

- `internal/application/booking/service.go` — Stage 4's BookingService is exactly what Stage 3 needs (Lua + XADD + metadata-missing repair). Wired via `fx.Provide(booking.NewService)` + `fx.As(new(booking.Service))`.
- `internal/application/worker/` — Stage 4's worker (`OrderMessageProcessor` + metrics decorator + `Service`) is wired inline in Stage 3's `installServer` via the same closure pattern Stage 4 uses (no `worker.Module`; the package doesn't export an fx options bundle).
- `internal/infrastructure/cache/lua/deduct.lua` — same script. Stage 3 has no need for Stage 2's `deduct_sync.lua` variant.
- `internal/infrastructure/api/stagehttp/` — `HandleBook` / `HandleGetOrder` / `HandlePayIntent` / `HandleTestConfirm` reused across Stages 1-3.

What's NEW in Stage 3:
- `cmd/booking-cli-stage3/main.go` + `server.go` — fx wiring + /events handler + `stage3Compensator` + sweeper + `compensateDanglingEvent`.
- `test/integration/postgres/stage3_booking_test.go` — 7 tests including the bidirectional PG-symmetry assertion (worker decrements ↔ compensator increments).
- Refreshed comments in `cmd/booking-cli-stage1/server.go` + `internal/infrastructure/api/stagehttp/compensator.go` to reflect the now-settled per-stage compensator semantics.

### PG-symmetry rule — the load-bearing invariant (DIFFERENT from Stage 2)

The critical Stage 3 design rule is **scoped**:

- **`stage3Compensator.Compensate`** (sweeper / `HandleTestConfirm` outcome=failed): MUST `UPDATE event_ticket_types SET available_tickets = available_tickets + qty`. The worker decremented → the compensator increments.
- **Worker `handleFailure`** (per-message DLQ path, e.g. 23505 INSERT failure): MUST NOT call `IncrementTicket`. The UoW rollback already undid the decrement atomically; explicit increment would double-count → permanent +qty drift.

The integration test pins both directions:

- `TestStage3Booking_AbandonCompensator_PGSymmetry`: book → wait for worker → compensate → assert PG column back at seeded value (and Redis qty back at seeded value, and order status='compensated').
- `TestStage3Booking_WorkerHandleFailure23505_PGUnchanged`: book once → wait for worker → book duplicate same user → wait for worker UoW rollback → assert PG column UNCHANGED at the post-first-book value (NOT the seeded value, NOT inflated by an erroneous handleFailure increment), and Redis qty restored.

If a future contributor misreads "MUST increment PG" as a global rule and adds `IncrementTicket` to `handleFailure`, the second test fails loudly. Symmetric for the AbandonCompensator test.

### Defense-in-depth: NullUUID + RowsAffected guards

Two defensive guards in `stage3Compensator.Compensate` (caught by Slice 1 multi-agent code review and applied to both Stage 2 and Stage 3):

1. **NullUUID scan**: `orders.ticket_type_id` is `UUID NULL`. Bare `uuid.UUID` scan would fail hard on a legacy NULL row with a non-`sql.ErrNoRows` error → bypasses the eligibility guard → spurious sweeper error logs. `uuid.NullUUID` + `.Valid` check returns `ErrCompensateNotEligible` cleanly.
2. **RowsAffected check on UPDATE event_ticket_types**: if the `ttID` doesn't match any `event_ticket_types.id` row (orphaned order — schema has no FK from `orders.ticket_type_id`, so cross-DB-drift / data corruption can produce this), the UPDATE silently no-ops. Without the rows-affected check, revert.lua + MarkCompensated would commit while PG inventory stayed short — permanent leak. The check turns the no-op into a hard error so the sweeper retries (revert.lua's SETNX guard short-circuits Redis re-INCRBY) and ops sees the recurring error.

The integration test `TestStage3Booking_CompensatorOrphanedTicketType_PGLeakGuard` pins the second guard.

### Failure modes Stage 3 inherits + adds

Inherited from Stage 4's worker pipeline:
- PEL recovery on restart (`worker.Service.Start` → `EnsureGroup` → `Subscribe` → `processPending` first).
- DLQ classifier for malformed messages (deterministic-failure fast path skipping retry budget).
- handleFailure compensation contract (revert.lua + DLQ XAdd; failure stays in PEL).
- handleParseFailure best-effort revert (Codex P1a from D4.1 follow-up).

Added by Stage 3 specifically:
- Sweeper-vs-PEL race silent-skip via the compensator's `sql.ErrNoRows` guard.
- Orphaned-ticket_type leak guard via the RowsAffected check.

Known limitation (acknowledged in plan §risks #5):
- `domain.ErrUserAlreadyBought` is NOT in `domain.IsMalformedOrderInput`'s sentinel list, so the worker treats 23505 as transient and burns `WORKER_MAX_RETRIES` × linear-backoff before routing to DLQ. Functionally correct (revert.lua restores Redis at the end; PG never permanently changes because each attempt's UoW rolls back), but wasteful — 3 PG transactions per duplicate user attempt. Inherited from Stage 4 verbatim; fix belongs in a post-D12 cleanup PR that touches Stage 4.

### How to run Stage 3

```bash
# Build
go build -o bin/booking-cli-stage3 ./cmd/booking-cli-stage3

# Run (requires Postgres + Redis)
DATABASE_URL='postgres://booking:smoketest_pg_local@localhost:5433/booking?sslmode=disable' \
  REDIS_PASSWORD=smoketest_redis_local REDIS_ADDR=localhost:6379 \
  PORT=8093 \
  BOOKING_RESERVATION_WINDOW=20s \
  EXPIRY_SWEEP_INTERVAL=5s \
  EXPIRY_GRACE_PERIOD=1s \
  bin/booking-cli-stage3 server
```

**Known dev-time foot-gun**: Stage 3 reads from `orders:stream` with the consumer group `orders:group`. Both names are package-level constants in `internal/infrastructure/cache/redis_queue.go` — NOT configurable. Running Stage 3 alongside Stage 4 (or any other binary using the same constants) against shared Redis causes silent message routing — workers compete and split the stream. Mitigation until PR-D12.5 (per-stage Redis DB index): only run ONE stage binary at a time against shared Redis. Tests are isolated via per-test testcontainers and unaffected.

### How Stage 3 was verified

| Verification | What it pins |
|---|---|
| **Service-level integration tests** ([`test/integration/postgres/stage3_booking_test.go`](../../test/integration/postgres/stage3_booking_test.go)) — 7 tests, ~13s total | HappyPath_AsyncWorkerInsertsOrder (full async flow with real worker goroutine; PG column AND Redis qty decrement by 1) / SoldOut / **AbandonCompensator_PGSymmetry** (compensator increments PG back to seeded value — the load-bearing rule) / **WorkerHandleFailure23505_PGUnchanged** (the inverse symmetry: UoW rollback restores PG; handleFailure must NOT IncrementTicket) / CompensatorAcceptsErrNoRows (sweeper-vs-PEL race silent-skip) / **CompensatorOrphanedTicketType_PGLeakGuard** (the new RowsAffected leak guard from Slice 1 H2) / ConcurrentContention (Lua + worker UoW serialization; PG column drains to 0) |
| **/events compensation tests** ([`cmd/booking-cli-stage3/compensate_test.go`](../../cmd/booking-cli-stage3/compensate_test.go)) — 2 tests | `compensateDanglingEvent` deletes both rows in FK-safe order + idempotent on missing rows |
| **Live HTTP smoke** | `make demo-up` stack + Stage 3 binary on `:8093`. All 3 k6 paths exercised: happy → paid; payment-failed → compensated + PG column back at seed + Redis qty restored; abandon → TTL → sweeper → same restoration |
| **Graceful shutdown across THREE goroutines** | Single shared `runCtx` coordinates HTTP + worker + sweeper. SIGTERM → fx OnStop chain → `cancel()` signals all three → `srv.Shutdown(stopCtx)` drains HTTP → `wg.Wait()` bounded by stopCtx for worker + sweeper. End-to-end ~6ms when both background goroutines are between work |
| **Multi-agent review pre-PR (no Codex due to limit)** | Plan reviewed by code-reviewer + silent-failure-hunter (8 findings actioned in plan revisions); per-slice reviews of Slice 1 (2 HIGH findings actioned: NullUUID + RowsAffected; same fixes applied to Stage 2) and Slice 2 (5 findings actioned: waitForRedisQty polling, FK comment correction, EnsureGroup scope clarification, user_id assertion, no-op compile assertion deletion) |

### Architectural cost Stage 3 surfaces vs Stage 2

The PG INSERT moves OFF the request hot path. `BookTicket` returns 202 after Lua + XADD (~1ms localhost). The worker drains the stream asynchronously, paying the PG round-trip latency offline. The expected throughput characteristic:

- Stage 2 hot path: Lua DECRBY + HMGET (~0.5ms) + PG INSERT + commit fsync (~3-8ms) → ~150-300 req/s per request thread (estimate; actual ceiling measured in PR-D12.5).
- Stage 3 hot path: Lua DECRBY + HMGET + XADD (~0.7ms) → 1000+ req/s estimated ceiling. PG INSERT throughput becomes a worker-side concurrency problem (single worker; multiple workers possible but not wired in PR-D12.3). The estimate is latency arithmetic, not measured against this PR's binary; PR-D12.5's `comparison.md` does the actual benchmarking.

The cost: **Stage 3 carries strictly more failure modes than Stage 2.** Async timing means a /book that returns 202 with an order_id may never produce a row (worker INSERT-fails → handleFailure → DLQ; client polls GET /orders/:id → 404 forever). Async timing also means the response shape is "eventually consistent" — the Pattern A reservation's reserved_until is computed at API-time, but if the worker is backpressured the order may already be expired by the time it lands in PG (the sweeper handles this correctly via `WHERE reserved_until <= NOW() - grace`). Per-message PEL recovery, parse-fail compensation, retry budget classification, sweeper-vs-PEL race — Stage 3 inherits all the failure-mode complexity of Stage 4's worker pipeline.

PR-D12.5's `comparison.md` will frame Stage 4 (with Kafka + saga) as the next layer of failure-mode complexity Stage 3 doesn't carry — the saga compensator handles cross-process compensation that Stage 3's in-binary sweeper handles in-process.

---

## Stage 4 — Pattern A end-to-end + saga compensator observability (PR-D12.4)

### Architecture

Stage 4 is the **current production binary** (`cmd/booking-cli`). The architecture itself was settled by D5–D7 + D4.1 follow-up; PR-D12.4's contribution is **making the failure-path saga compensator observable** so the comparison harness can frame Stage 4's incremental cost vs Stage 3 quantitatively.

```
POST /book   ─── inherited from Stage 3 verbatim ───
  EVAL deduct.lua → XADD orders:stream → 202 Accepted
  worker → uow.Do { TicketType.DecrementTicket + Order.Create }

POST /pay   ── D4 PaymentIntent (Stripe-shape; gateway-idempotent on order_id) ──
POST /webhook/payment   ── D5 inbound provider webhook (HMAC-verified) ──
  succeeded → MarkPaid
  payment_failed → MarkPaymentFailed + outbox INSERT (events_outbox)
                                              ↓
                                              ↓  ─── async ───
                                              ↓
                                  OutboxRelay (poll every 1s,
                                  pg_try_advisory_lock leader)
                                              ↓
                                  events_outbox.created_at  ─→  kafka.Message.Time
                                              ↓
                                  Kafka order.failed topic
                                              ↓
                                  SagaConsumer (in-process,
                                  group=booking-saga-group)
                                              ↓
                                  HandleOrderFailed(ctx, payload, originatedAt)
                                              ↓
                                  uow.Do { resolveTicketTypeID (Path A/B/C)
                                            → IncrementTicket
                                            → MarkCompensated }
                                              ↓
                                  RevertInventory (revert.lua, idempotent)
                                              ↓
                                  metrics.RecordEventProcessed(outcome)
                                  metrics.ObserveLoopDuration(time.Since(originatedAt))   ← only on `compensated`
                                  metrics.SetConsumerLag(time.Since(msg.Time))            ← every loop, every outcome

D6 expiry sweeper + recon force-fail emit the SAME `order.failed` outbox
shape — the saga compensator is path-agnostic to the producer.
```

### What Stage 4 adds vs Stage 3

| Layer | Stage 3 | Stage 4 |
|---|---|---|
| Failure-path compensation | In-binary `stage3Compensator` called by sweeper + `/test/payment/confirm/:id?outcome=failed` directly | Outbox event → Kafka `order.failed` → in-process saga consumer → `HandleOrderFailed` |
| Cross-process compensation guarantee | Same-binary mutex (sweeper goroutine vs HTTP handler — runtime contention) | At-least-once Kafka delivery + idempotent DB MarkCompensated + idempotent revert.lua (SETNX guard on compensation key) |
| Failure-path observability | Sweeper-level logs only (no histograms / counters / lag) | 3 throughput metrics + 13 exhaustive outcome labels + 4 alerts + 4 runbook sections |
| Money-movement architecture | `/test/payment/confirm/:id` direct compensator call (no real webhook) | D5 inbound provider webhook (HMAC-verified) + D4 PaymentIntent gateway adapter (gateway-idempotent on `order_id`) |
| Recovery surface | Sweeper-only (PG `WHERE reserved_until <= NOW()`) | Sweeper + recon + saga watchdog + saga consumer PEL recovery (Kafka group offset commit) |

### Saga compensator throughput observability (the PR-D12.4 contribution)

Three new Prometheus metrics plus 13 outcome-label exhaustiveness:

| Metric | Type | Labels | What it measures |
|---|---|---|---|
| `saga_compensator_events_processed_total` | Counter | `outcome` | One increment per `order.failed` event consumed; the `outcome` label classifies WHY the compensator returned (success, idempotent re-drive, classified error, or `unknown` sentinel) |
| `saga_compensation_loop_duration_seconds` | Histogram | (none) | E2E latency from `events_outbox.created_at` to `MarkCompensated` commit. **Observed ONLY on the `compensated` success path** — `already_compensated` is sub-millisecond no-op work and would skew p50/p99 to the floor. Buckets `0.05s … 300s` (failure-path latency can be minutes due to retries) |
| `saga_compensation_consumer_lag_seconds` | Gauge | (none) | `time.Since(kafka.Message.Time)` per loop iteration; companion goroutine zeroes the gauge after 30s of no messages so a quiet system reads 0 instead of stuck-at-last-known |

The 13 outcome labels are **pre-warmed at boot** (`metrics_init.go`) so PromQL alerts evaluate `rate(...{outcome="X"}[5m])` from second 0, not from "first time we observed X". The full label set:

| Label | Path | Histogram observed? |
|---|---|---|
| `compensated` | Full happy path: GetByID → IncrementTicket → MarkCompensated → RevertInventory all OK | ✓ |
| `already_compensated` | Kafka at-least-once redelivery; order already in `compensated` state; Redis revert.lua succeeds (idempotent) | ✗ |
| `already_compensated_redis_error` | Same as above but RevertInventory failed — distinguishes benign idempotent re-drive blip from FRESH compensation Redis failure (real inventory leak signal) | ✗ |
| `path_c_skipped` | Legacy `uuid.Nil` payload + 0 or > 1 ticket_types per event (D4.1 rolling-upgrade tail or D8 multi-ticket future). Inside the UoW: `IncrementTicket` skipped, `MarkCompensated` still applied (the order is no longer recoverable to a charging path either way). After UoW: Redis revert skipped (no resolved ticket_type to scope the runtime key) | ✗ |
| `unmarshal_error` | `OrderFailedEvent` JSON parse failed; payload routed to be re-delivered (consumer-side dead-letter is a future PR) | ✗ |
| `getbyid_error` / `list_ticket_type_error` / `incrementticket_error` / `markcompensated_error` | Per-step `*stepError` wrapper from inside the UoW closure; classified by `outcomeForStepError` via `errors.As`, NOT string matching | ✗ |
| `redis_revert_error` | FRESH (not idempotent re-drive) Redis revert failure — paired alert `SagaCompensatorRedisInventoryLeak` fires on `rate > 0.1/s for 2m` | ✗ |
| `context_error` | UoW returned `context.Canceled` / `context.DeadlineExceeded` — separates shutdown noise from real failures so ops can filter | ✗ |
| `uow_infra_error` | UoW returned a non-stepError, non-context error (tx-begin failure, connection-pool exhaustion) | ✗ |
| `unknown` | **Deferred sentinel** — fired only by the `did_record` closing-defer if a future contributor adds a return path that forgets to call `record(outcome)`. Paired alert `SagaCompensatorClassifierDrift` fires on `rate > 0` | ✗ |

The classifier is in `internal/application/saga/compensator.go:36-50` (`outcomeForStepError`) and uses `errors.As` against the typed `*stepError` wrapper — string matching the error message would be brittle to error-message-prose tweaks.

### E2E latency data path (Slice 0 substrate)

The histogram measures **end-to-end** latency, not just compensator-internal work. Threading the timestamp through the pipeline is what makes that possible:

```
events_outbox.created_at   ← factory-assigned at outbox INSERT time
        ↓                    (NOT DB-assigned; preserves timestamp through relay restart)
OutboxEvent.CreatedAt()    ← rehydration via ReconstructOutboxEvent in postgres scan
        ↓
kafka.Message.Time         ← OutboxRelay sets this on publish via messageForOutboxEvent
        ↓                    (defense-in-depth zero-time guard rejects publish at boundary)
HandleOrderFailed(ctx, payload, originatedAt time.Time)
        ↓
metrics.ObserveLoopDuration(time.Since(originatedAt))   ← only on compensated path
```

`originatedAt` is wired through to `HandleOrderFailed` as an explicit argument (NOT pulled from a goroutine-local) so the saga watchdog (which re-drives stuck `failed` orders without a fresh Kafka message) can pass `time.Now()` without conflating watchdog-driven latency with consumer-driven latency.

### 4 new alerts

| Alert | Condition | Severity | Rationale |
|---|---|---|---|
| `SagaCompensatorErrorRate` | `(sum(rate(...{outcome!~"compensated\|already_compensated\|already_compensated_redis_error"}[5m])) / sum(rate(...[5m]))) > 0.05` for `5m` | warning | **Ratio**, not absolute rate — non-success outcomes / total outcomes. Threshold `0.05` (5%). `already_compensated_redis_error` is excluded because Kafka at-least-once redelivery bursts are benign idempotent re-drives — the `SagaCompensatorRedisInventoryLeak` alert below covers the FRESH-compensation Redis failure separately |
| `SagaConsumerLagHigh` | `saga_compensation_consumer_lag_seconds > 30` for `2m` | warning | Either Kafka group offset is stuck (consumer wedged) or outbox-relay-to-Kafka path is backed up. 30s aligns with `idleResetThreshold` so quiet-system false positives can't sustain — the companion goroutine zeroes the gauge after 30s of no messages |
| `SagaCompensatorClassifierDrift` | `increase(saga_compensator_events_processed_total{outcome="unknown"}[5m]) > 0` for `0m` | warning | Fires immediately on any single `unknown` increment — `unknown` is the deferred-sentinel, fired ONLY if a future PR adds a return path that forgot `record(outcome)`. Pre-warmed at boot so the alert evaluates the time series from second 0 |
| `SagaCompensatorRedisInventoryLeak` | `increase(saga_compensator_events_processed_total{outcome="redis_revert_error"}[5m]) > 0` for `0m` | critical | Fires immediately on any single `redis_revert_error` increment. FRESH compensation Redis failure means `MarkCompensated` committed but `revert.lua` did NOT — permanent inventory leak. Paired runbook covers `revert.lua` rollback playbook |

All four alerts have runbook sections under `docs/runbooks/README.md` (silent-failure-hunter Slice 3 CRITICAL — alert anchors must resolve, not 404).

### Deployment expectation: +Inf histogram spike on first PR-D12.4 deploy

The `+Inf` spike risk is narrow and time-bounded — it concerns **pre-PR Kafka messages in flight**, NOT pre-PR DB rows.

- **Pre-PR DB rows are fine.** `events_outbox.created_at` is schema-defaulted to `NOW()` at INSERT time, so any pre-PR row still in `pending` state when the new relay code runs gets read with a real timestamp and threaded into `kafka.Message.Time` correctly.
- **Pre-PR Kafka messages already published are the problem.** Pre-PR-D12.4, the publisher did NOT set `kafka.Message.Time` (it took raw `(topic, payload)` only, not the OutboxEvent — see the `EventPublisher.Publish` → `EventPublisher.PublishOutboxEvent` API change). Any message published BEFORE the deploy and consumed AFTER the deploy arrives at the new consumer with `kafka.Message.Time = zero` → `time.Since(zero)` ≈ 56 years → lands in the `+Inf` histogram bucket.

Operational impact:
- p99 of `saga_compensation_loop_duration_seconds` will spike to **absurd values** for the duration of pre-PR Kafka backlog drain.
- `SagaCompensatorErrorRate` does NOT fire (the `compensated` outcome label is excluded; backlog drain still produces success outcomes).
- `SagaConsumerLagHigh` MAY fire transiently if backlog is large — that's the actual signal worth investigating; the histogram spike is noise.

Mitigation:
- Defense-in-depth zero-CreatedAt guard at the **publish** boundary (`kafka_publisher.go`) rejects any new publish with zero CreatedAt → forward path is clean post-deploy.
- Pre-deploy runbook (TODO post-D12.4): drain `order.failed` topic backlog before deploying or accept the histogram spike as expected one-off noise.

### How to run Stage 4

```bash
# Build
go build -o bin/booking-cli ./cmd/booking-cli

# Run (requires Postgres + Redis + Kafka)
DATABASE_URL='postgres://booking:smoketest_pg_local@localhost:5433/booking?sslmode=disable' \
  REDIS_PASSWORD=smoketest_redis_local REDIS_ADDR=localhost:6379 \
  KAFKA_BROKERS=localhost:9092 \
  PORT=8094 \
  BOOKING_RESERVATION_WINDOW=20s \
  EXPIRY_SWEEP_INTERVAL=5s \
  EXPIRY_GRACE_PERIOD=1s \
  bin/booking-cli server
```

Same `make demo-up`-style env-var compatibility as Stages 1–3. Per-stage Postgres DB isolation is still PR-D12.5; until then run only ONE stage binary at a time against the shared backing services.

### How Stage 4 (PR-D12.4) was verified

| Verification | What it pins |
|---|---|
| **Compensator outcome-label exhaustive tests** ([`internal/application/saga/compensator_test.go`](../../internal/application/saga/compensator_test.go)) — `TestHandleOrderFailed_OutcomeLabel_Exhaustive` 12 subtests + `TestHandleOrderFailed_UnmarshalError_RecordsOutcome` standalone | Every outcome label has at least one direct test. Each subtest asserts (1) exactly-one outcome recorded, (2) the outcome matches the expected branch, (3) `unknown` sentinel NOT tripped, (4) histogram observed iff `compensated` (no skew from `already_compensated` no-ops). Path-C × already-compensated cross-product covered (Slice 4 review M2) |
| **shouldResetLag boundary tests** ([`internal/infrastructure/messaging/saga_consumer_test.go`](../../internal/infrastructure/messaging/saga_consumer_test.go)) — `TestShouldResetLag_TableDriven` 7 subtests | Pure-function decision pinned across the matrix: `lastUnixNano==0` sentinel returns true; `<30s ago` doesn't reset; `==30s ago` (boundary equality) doesn't reset (`>` not `>=`); `>30s ago` resets; `1h ago` resets. References production `idleResetThreshold` constant directly so a future tweak fails the test (M1 review fix) |
| **runIdleReset goroutine lifecycle** — `TestRunIdleReset_ExitsOnCtxDone` (200ms timeout) | Companion goroutine returns within 200ms of ctx cancel → no goroutine leak per Start invocation. The 5s ticker × 30s threshold real-time integration test is deferred (`TODO(d12-test-debt)`) — promoting `idleResetThreshold` to `var` is the documented path forward |
| **Kafka publisher zero-CreatedAt guard** ([`internal/infrastructure/messaging/kafka_publisher_test.go`](../../internal/infrastructure/messaging/kafka_publisher_test.go)) | Publisher rejects publish at the boundary if `OutboxEvent.CreatedAt()` is zero — defense-in-depth against +Inf histogram spike from pre-PR rows. Ensures the relay errors loudly rather than silently corrupting the histogram |
| **Outbox row created_at scan + roundtrip** ([`internal/infrastructure/persistence/postgres/outbox_row_test.go`](../../internal/infrastructure/persistence/postgres/outbox_row_test.go) + [`test/integration/postgres/outbox_repository_test.go`](../../test/integration/postgres/outbox_repository_test.go)) | The `created_at` column survives INSERT → SELECT → rehydrate via `ReconstructOutboxEvent`; relay sees the original timestamp not `NOW()` |
| **Multi-agent review pre-PR (no Codex due to limit)** | Per-slice reviews by silent-failure-hunter + go-reviewer + code-reviewer (no Codex due to rate limit). Slice 1: 4 findings (HIGH watchdog binary missing fx provider — would crash at startup; typo `listticketttype_error` × 2 files; pre-warm scope). Slice 2: 7 findings (UoW classifier `unknown` overload; `already_compensated_redis_error` distinguisher; `originatedAt` arg threading; atomic.Int64 over atomic.Pointer; +3). Slice 3: 7 findings (CRITICAL 4 runbook anchors 404; HIGH `already_compensated_redis_error` excluded from error-rate ratio; +5). Slice 4: 4 findings (M1 const reference; M2 path-C × already-compensated subtest; 2× P2 doc gaps). All actioned before push |

### Architectural cost Stage 4 surfaces vs Stage 3

The saga path adds **cross-process latency** (outbox poll + Kafka round-trip) on the failure path AND adds **operational complexity** (Kafka broker + topic provisioning + group offset management + classifier outcome label maintenance). Every new return path the compensator adds in the future MUST classify or trip the `unknown` sentinel — drift is expensive but visible, not silent.

What the comparison harness will quantify in PR-D12.5:
- Stage 3 abandon-compensation latency: sweeper tick interval (configurable, ~5s typical) + in-binary compensator (~10ms).
- Stage 4 abandon-compensation latency: outbox poll interval (1s) + Kafka publish + consumer group fetch + in-process compensator (~10ms) + ObserveLoopDuration histogram captures the actual distribution.

The expected verdict (NOT measured against this PR's binary; PR-D12.5 does the actual benchmarking): Stage 3 has lower median compensation latency; Stage 4 has higher complexity ceiling but observable failure modes with paired alerts. The framing PR-D12.5's `comparison.md` will use: Stage 4 buys cross-process compensation guarantees + multi-binary scaling headroom (the saga consumer is independently scalable from the booking API) at the cost of one Kafka round-trip per compensation event.

---

## What's in PR-D12.1

```
internal/application/booking/sync/service.go            (~270 LOC)
test/integration/postgres/sync_booking_test.go          (~330 LOC, 5 tests)
cmd/booking-cli-stage1/main.go                          (~70 LOC)
cmd/booking-cli-stage1/server.go                        (~570 LOC)
docs/d12/README.md                                      (this file)
```

Total: ~1.2k LOC across 8 commits. Codex review rounds: 2 plan-rounds + 5 code-rounds (slices 1, 3, 4, 5 each got at least one P2 fix; slice 2 was clean).

## What's in PR-D12.2

```
internal/infrastructure/cache/lua/deduct_sync.lua       (~80 lines)
internal/infrastructure/cache/sync_lua_deducter.go      (~140 LOC)
internal/application/booking/synclua/service.go         (~250 LOC)
internal/application/booking/synclua/service_test.go    (~280 LOC, 11 tests)
cmd/booking-cli-stage2/main.go                          (~70 LOC)
cmd/booking-cli-stage2/server.go                        (~370 LOC; +compensateDanglingEvent helper from Codex round-1 P2)
cmd/booking-cli-stage2/compensate_test.go               (~70 LOC, 2 tests)
test/integration/postgres/redis_harness.go              (~100 LOC; new Redis testcontainer)
test/integration/postgres/synclua_booking_test.go       (~390 LOC, 6 tests)
docs/d12/README.md                                      (Stage 2 section — this file)
```

Total: ~1.7k LOC. Tests outnumber production code by ~1.5×, matching Stage 1's verification posture. Codex round-1 caught two findings (P1 PG inflation in compensator + P2 dangling 201 on Redis hydrate failure); both actioned as fixup before push.

## What's in PR-D12.3

```
cmd/booking-cli-stage3/main.go                          (~85 LOC; Cobra wrapper)
cmd/booking-cli-stage3/server.go                        (~565 LOC; fx wiring + /events handler + stage3Compensator + sweeper + compensateDanglingEvent)
cmd/booking-cli-stage3/compensate_test.go               (~70 LOC, 2 tests)
test/integration/postgres/stage3_booking_test.go        (~565 LOC, 7 tests including bidirectional PG-symmetry pair + orphaned-ticket_type leak guard)

MODIFIED:
internal/infrastructure/api/stagehttp/compensator.go    (refreshed Compensator interface doc to describe per-stage settled designs)
cmd/booking-cli-stage1/server.go                        (refreshed stale Stage 2/3 comparison comment)
cmd/booking-cli-stage2/server.go                        (NullUUID scan + sql.ErrNoRows guard fixup; same H1 fix as Stage 3)
docs/d12/README.md                                      (new Stage 3 section — this file)
```

Total: ~1.4k LOC. Smaller than PR-D12.2 because Stage 3 reuses Stage 4's `booking.Service` + `worker` package + `deduct.lua` verbatim — only the cmd binary + compensator + integration tests are new. Multi-agent review pre-PR (no Codex due to limit) caught 8 plan-stage findings + 2 Slice 1 HIGH findings (NullUUID, RowsAffected — applied to Stage 2 too) + 5 Slice 2 findings; all actioned before push.

## What's in PR-D12.4

```
ADDED:
internal/application/saga/metrics.go                          (CompensatorMetrics interface + NopCompensatorMetrics)
internal/infrastructure/observability/metrics_saga.go         (3 prom vars: counter + histogram + gauge)
internal/infrastructure/messaging/saga_consumer_test.go       (~150 LOC, 7 shouldResetLag subtests + 1 runIdleReset lifecycle test)
internal/infrastructure/messaging/kafka_publisher_test.go     (zero-CreatedAt boundary guard tests)

MODIFIED:
internal/domain/event.go                                      (OutboxEvent.CreatedAt() accessor + factory-assigned)
internal/infrastructure/persistence/postgres/outbox_row.go    (created_at column scan)
internal/infrastructure/persistence/postgres/repositories.go  (outbox INSERT explicit created_at = $5)
internal/infrastructure/persistence/postgres/outbox_row_test.go    (created_at coverage)
test/integration/postgres/outbox_repository_test.go           (created_at SQL roundtrip)
internal/application/messaging.go                             (EventPublisher.PublishOutboxEvent replaces Publish)
internal/application/outbox/relay.go + relay_test.go          (call new PublishOutboxEvent)
internal/infrastructure/messaging/kafka_publisher.go          (messageForOutboxEvent helper + zero-CreatedAt guard)
internal/mocks/messaging_mock.go                              (regen)
internal/application/saga/compensator.go                      (typed *stepError + outcomeForStepError + originatedAt arg + did_record sentinel + 13 record() callsites)
internal/application/saga/compensator_test.go                 (recordingCompensatorMetrics spy + 12 outcome subtests + 1 unmarshal_error standalone)
internal/application/saga/watchdog.go + watchdog_test.go      (passes time.Now() as originatedAt)
internal/infrastructure/messaging/saga_consumer.go            (lastMessageAt atomic.Int64 + lag emit + runIdleReset companion + shouldResetLag pure helper)
internal/infrastructure/observability/metrics_init.go         (13 outcome labels pre-warmed)
internal/bootstrap/sweeper_adapters.go                        (prometheusCompensatorMetrics adapter + NewPrometheusCompensatorMetrics)
cmd/booking-cli/server.go                                     (fx.Provide NewPrometheusCompensatorMetrics)
cmd/booking-cli/saga_watchdog.go                              (same fx provider; would crash at startup without it — Slice 1 HIGH fix)
deploy/prometheus/alerts.yml                                  (4 new alerts)
docs/monitoring.md + docs/monitoring.zh-TW.md                 (§2 metric inventory + §5 alert catalog; bilingual structural parity)
docs/runbooks/README.md                                       (4 new runbook sections; closes Slice 3 CRITICAL anchor 404)
docs/d12/README.md                                            (Stage 4 section — this file)
```

Total: ~2.1k LOC including tests. Tests outnumber production saga code by ~2× (12 outcome subtests + spy infrastructure + 8 saga consumer tests + boundary guard tests). Multi-agent review pre-PR (no Codex due to limit): 8 plan-stage findings + 4 Slice 1 + 7 Slice 2 + 7 Slice 3 + 4 Slice 4 = 30 findings actioned across 5 review rounds.

## What's in PR-D12.5

```
ADDED:
deploy/postgres/init/02_create_stage_dbs.sql          (per-stage DB provisioning, idempotent \gexec)
docker-compose.comparison.yml                          (9 services on non-conflicting ports)
deploy/prometheus/prometheus.comparison.yml            (per-stage scrape labels: stage + architecture)
scripts/run_4stage_comparison.sh                       (orchestration: pre-flight → bench-up → smoke → k6 × 4 → snapshot → tear-down)
scripts/generate_comparison_md.py                      (k6 summary.json → comparison.md emitter; 6-citation map)
docs/benchmarks/comparisons/<TS>_4stage_c500_d60s/     (the canonical run output)

MODIFIED:
Dockerfile                                             (BUILD_TARGET arg; backward-compatible default ./cmd/booking-cli)
Makefile                                               (bench-up / bench-down / bench-down-clean / bench-smoke targets)
docs/d12/README.md                                     (this file: status closure + What's in section)
docs/post_phase2_roadmap.md                            (D12 marked closed)
README.md + README.zh-TW.md                            (D12 cross-link to comparison.md; bilingual)
docs/PROJECT_SPEC.md + docs/PROJECT_SPEC.zh-TW.md      (Stage 4 evolution note: now-past-tense for D12 benchmarking; bilingual)
```

Total: ~1.5k LOC including the orchestration + helper scripts. No application-code changes — all 4 stage binaries already exist. Multi-agent review pre-PR (no Codex due to limit): plan-stage 19 findings + per-slice reviews (Slice 0: 3 findings; Slice 1: zero — empirically verified; Slice 2: 5 findings; Slice 3: 9 findings incl. CRITICAL cleanup-trap; Slice 4: 12 findings incl. 3 CRITICAL citation venue errors). All actioned before PR-open.

The `make bench-smoke` target (VUS=1, DURATION=10s) is the CI rot-prevention path: runs in CI as a non-gating job to detect harness drift without paying the full 60s × 4-stage benchmark cost on every push.

**Citation map**: 9 verified citations in the comparison.md (5 industry sources + 4 peer-reviewed). Slice 4 review caught fabrications in the original 7-paper plan (Faleiro PVLDB'17 → CIDR'17, Atikoglu SIGMOD'12 → SIGMETRICS'12, Kløvedal 2025 preprint dropped as unverifiable). Slice 7 added 3 funnel-methodology citations (Ticketmaster Q1'23 / Stripe Q3'24 / Shopify BFCM'24) when the headline metric was migrated from aggregate `http_reqs/s` to **booking-intake RPS** — see Slice 7 below.

**Funnel-stage methodology (Slice 7)**: the canonical headline number is **booking intake RPS** — `accepted_bookings.rate` (POST /book → 202), NOT aggregate `http_reqs.rate`. Aggregate http_reqs mixes intake with payment-intent calls and status polls, obscuring the architectural-cost signal. This split mirrors Ticketmaster's "tickets sold per second" published metric, Stripe's separate PaymentIntent-creation-rate vs settlement-rate reporting, and Shopify's BFCM 2024 distinction between total request rate and accepted-orders rate. The funnel decomposition section in `comparison.md` shows all 3 layers (intake → authorization → settlement) plus per-layer p95 latency.

## Cross-references

- D12 plan with research-informed adjustments: `~/.claude/plans/d12-multi-stage-comparison.md` (local plan file)
- Pattern A blog post (D15): [`docs/blog/2026-05-saga-pure-forward-recovery.zh-TW.md`](../blog/2026-05-saga-pure-forward-recovery.zh-TW.md) ([EN](../blog/2026-05-saga-pure-forward-recovery.md))
- Two-step k6 baseline (D9): [`docs/benchmarks/20260509_014318_two_step_baseline_c100_d90s/comparison.md`](../benchmarks/20260509_014318_two_step_baseline_c100_d90s/comparison.md)
- Roadmap: [`docs/post_phase2_roadmap.md`](../post_phase2_roadmap.md) D12 section
