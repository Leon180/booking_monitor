# D12 — 4-stage architecture comparison harness

> Status: **PR-D12.1 (Stage 1) shipped.** Stages 2–4 + the multi-target harness land in PR-D12.2 through PR-D12.5.

D12 is the senior-portfolio centerpiece of Phase 3 (per [`docs/post_phase2_roadmap.md`](../post_phase2_roadmap.md) L104-L111). Four separate Go binaries share the same `internal/` packages but use different fx wirings and different `booking.Service` implementations — running the same workload across all four under [`scripts/k6_two_step_flow.js`](../../scripts/k6_two_step_flow.js) produces a side-by-side benchmark table that quantifies each architectural decision's cost vs. headroom.

## The 4 stages

| Stage | `cmd/` binary | Architecture | What it adds vs. previous |
|---|---|---|---|
| **1** | `cmd/booking-cli-stage1/` | API → Postgres `BEGIN; SELECT FOR UPDATE; UPDATE event_ticket_types; INSERT orders; COMMIT;` | (baseline) |
| 2 | `cmd/booking-cli-stage2/` (PR-D12.2) | API → Redis Lua atomic deduct → SYNCHRONOUS PG INSERT | Redis hot-path inventory, no async buffering |
| 3 | `cmd/booking-cli-stage3/` (PR-D12.3) | API → Redis Lua → `orders:stream` → worker → DB INSERT | async via stream + worker; no event-driven downstream |
| 4 | `cmd/booking-cli/` (current; +observability in PR-D12.4) | API → Redis Lua → `orders:stream` → worker → DB INSERT; **money movement via** `/pay` + D5 webhook (not Kafka-driven post-D7); `order.failed` outbox → Kafka → in-process saga compensator (D5 webhook `payment_failed` + D6 expiry sweeper + recon force-fail as the only producers) | full Pattern A end-to-end with saga compensation only on the failure path |

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

## What's in PR-D12.1

```
internal/application/booking/sync/service.go            (~270 LOC)
test/integration/postgres/sync_booking_test.go          (~330 LOC, 5 tests)
cmd/booking-cli-stage1/main.go                          (~70 LOC)
cmd/booking-cli-stage1/server.go                        (~570 LOC)
docs/d12/README.md                                      (this file)
```

Total: ~1.2k LOC across 8 commits. Codex review rounds: 2 plan-rounds + 5 code-rounds (slices 1, 3, 4, 5 each got at least one P2 fix; slice 2 was clean).

## Future PRs (D12.2 through D12.5)

| PR | Scope | Effort |
|---|---|---|
| **PR-D12.2** | Stage 2 binary (Redis Lua + sync PG INSERT) — reuses Stage 1's contract; adds Redis seam + Redis revert in compensation | ~3 days |
| **PR-D12.3** | Stage 3 binary (Lua + stream + worker, no outbox) — likely small if Stage 4's worker is reusable with a different fx wiring | ~3-4 days |
| **PR-D12.4** | Stage 4 (current cmd/booking-cli) + observability changes: Prometheus scrape-config `stage` target labels for ALL 4 binaries; new saga-throughput metrics (`saga_compensator_events_processed_total{outcome}`, `saga_compensation_loop_duration_seconds` histogram with `events_outbox.created_at → MarkCompensated commit` data path via `kafka.Message.Time`, `saga_compensation_consumer_lag` gauge) | ~3 days |
| **PR-D12.5** | Harness + `comparison.md`: per-stage Postgres DB + per-stage Redis DB index; docker-compose comparison profile; orchestration script; first comparison.md with the full 6-paper citation map (Psarakis CIDR'25 / SIGMOD'25 tutorial / Faleiro PVLDB'17 / Cheng PVLDB'24 / Laigner TOSEM'25 / Kløvedal preprint) | ~5-7 days |

## Cross-references

- D12 plan with research-informed adjustments: `~/.claude/plans/d12-multi-stage-comparison.md` (local plan file)
- Pattern A blog post (D15): [`docs/blog/2026-05-saga-pure-forward-recovery.zh-TW.md`](../blog/2026-05-saga-pure-forward-recovery.zh-TW.md) ([EN](../blog/2026-05-saga-pure-forward-recovery.md))
- Two-step k6 baseline (D9): [`docs/benchmarks/20260509_014318_two_step_baseline_c100_d90s/comparison.md`](../benchmarks/20260509_014318_two_step_baseline_c100_d90s/comparison.md)
- Roadmap: [`docs/post_phase2_roadmap.md`](../post_phase2_roadmap.md) D12 section
