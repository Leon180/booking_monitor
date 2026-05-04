# Architectural Backlog

Items deliberately **parked** with a documented rationale — distinct from [`docs/post_phase2_roadmap.md`](post_phase2_roadmap.md) (which sequences active work). An item lives here when:

- A reviewer flagged it but the project chose to defer.
- A future trigger condition (scale milestone, demo phase, real incident) determines when to revisit.
- The current implementation has a known limitation that's acceptable for now.

Each entry: **what**, **why deferred**, **revisit when**. Dated so future triage knows whether the rationale still holds.

---

## Resilience patterns

### Bulkhead pattern (per-dependency semaphore)
- **What.** Per-resource concurrency limiters around DB / Redis / Kafka / payment-gateway calls so one degraded dependency can't exhaust the whole goroutine pool.
- **Why deferred.** With the circuit breakers planned in roadmap A10* (Redis + DB + Kafka + payment) plus the DB pool gauge from N1, bulkheads become incremental defense-in-depth rather than load-bearing protection. Adding them now is preemptive optimization.
- **Revisit when.** The first incident that traces back to "one stuck dependency starved the goroutine pool". Until then, A10* + N1 are the primary signals.

### Feature flag system
- **What.** A controlled-rollout primitive (LaunchDarkly-style) for gradual feature enablement.
- **Why deferred.** No business need yet — every change ships through PR review and atomic deploys. Feature flags are framework-grade infrastructure for teams running A/B tests at scale; out of proportion for the current scope.
- **Revisit when.** First time a risky rollout (e.g. inventory sharding B3) needs gradual enablement. At that point, evaluate `growthbook` vs hand-rolled boolean env vars.

---

## Observability and operations

### Promauto global registry coupling
- **What.** All metric registrations go through `promauto.NewX(...)` which uses `prometheus.DefaultRegisterer` — a process global. Tests that read globals can race across packages (CP1 saw this; CP2 fixed it via the `recordingMetrics` application-layer fakes).
- **Why deferred.** The application-layer fakes already give test isolation. Replacing globals with an injected `*Registry` struct is moderate cost (every metric-emitter takes `*Metrics` as a parameter) for a benefit that only matters if a future test starts reading globals directly.
- **Revisit when.** A CI flake the application-layer fakes don't catch — likely never, but the design escape hatch exists.

### Per-job severity for `TargetDown`
- **What.** Currently `up == 0` fires `severity=critical` for every scrape target. Future setup with infra exporters (postgres-exporter, redis-exporter, blackbox-exporter) might want non-critical exporters to fire `severity=warning` instead.
- **Why deferred.** No infra exporters exist yet — we only scrape application processes today. Per-job severity routing is documented forward-compatibly in the alert comment.
- **Revisit when.** First infra exporter scrape job lands. At that point, either split the alert into per-job rules or add an Alertmanager route override.

### SLO + multi-window burn-rate alerts
- **What.** SRE Workbook Ch.5 pattern: define error-budget SLOs, alert on burn-rate windows (1h fast burn + 5m fast burn → page; 6h slow burn + 30m slow burn → ticket).
- **Why deferred.** Current alerts are static-threshold. Burn-rate alerts require an explicit SLO (currently undefined for this simulator).
- **Revisit when.** Phase 3 demo needs to position the system as production-shaped. SLOs + burn-rate alerts are part of the senior-grade defensibility story alongside auth (N9) and circuit breakers (A10*).

### `benchmark_compare.sh` extractor for `accepted_bookings`
- **What.** The auto-generated `comparison.md` shows `Booking accepted | N/A` because the extractor doesn't know about the new `accepted_bookings` Counter (added in PR #66). Raw `run_a_raw.txt` / `run_b_raw.txt` files capture the data correctly.
- **Why deferred.** Reporting-layer issue, not data loss. Easy to fix in a small follow-up commit.
- **Revisit when.** Next time a benchmark run is recorded for a hot-path PR — the operator will want the headline column populated.

---

## Persistence layer

### `IdempotencyResult` / `IdempotencyRepository` relocation
- **What.** Currently `domain.IdempotencyResult` describes HTTP replay data — a transport contract sitting in domain. The senior-review checkpoint flagged this as a layer violation candidate.
- **Why deferred.** The relocation is a moderate refactor (moves the type, updates 4-5 call sites). Today's placement works because no other domain consumer cares about the type.
- **Revisit when.** Next time the idempotency contract changes substantively (e.g. multi-key fingerprint coalescing). Relocate as part of that PR rather than as a standalone shuffle.

### `redisOrderQueue` god-object split
- **What.** `redisOrderQueue` owns subscription, PEL recovery, retry, compensation, DLQ writing, metrics, and parsing. Too much for one type.
- **Why deferred.** The pieces have evolved together; a clean split needs more design than fits a cleanup PR.
- **Revisit when.** Next substantive change to any of those concerns — split out the touched concern as part of that PR.

### Postgres repositories single-file split
- **What.** All three repos (Order/Event/Outbox) live in `repositories.go` (~500 lines). Splitting by aggregate would reduce merge friction.
- **Why deferred.** No active blocker; the file isn't growing fast enough to warrant a layout PR on its own.
- **Revisit when.** A future PR adds a fourth repository, or ownership clarity matters more than file-count cost.

### Cache-truth architecture: Redis as ephemeral, DB as source of truth (2026-05-03)

- **What.** A conservation-invariant check during the O3.1b saturation profile uncovered: under `make reset-db` + `FLUSHALL` followed by load, **411 of 1000 successful Lua deducts had no corresponding DB row, no DLQ entry, and no log trace**. Root cause traced to [`redis_queue.go`'s NOGROUP self-heal](../internal/infrastructure/cache/redis_queue.go#L201) calling `XGROUP CREATE ... $`, which silently skips messages that arrived before the recreation. Walked the trace end-to-end: timeline of XADDs, consumer-group recreation, and `last-delivered-id` confirms the bug is real but **only triggered in test environments that FLUSHALL during active production**.

- **Industry consensus** ([antirez](https://redis.antirez.com/fundamental/streams-consumer-patterns.html), [Alibaba 秒殺 docs](https://www.alibabacloud.com/help/en/redis/use-cases/use-apsaradb-for-redis-to-build-a-business-system-that-can-handle-flash-sales), [Stripe idempotency post](https://stripe.com/blog/idempotency), [AWS ElastiCache](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/RedisAOF.html), [redis/redis#10630](https://github.com/redis/redis/issues/10630)):
  - **DB is source of truth, Redis is ephemeral cache.** Treat Redis as rehydratable from DB at any time.
  - **Initial population: at event-creation write + bulk rehydrate at app startup** (with advisory lock to prevent multi-app dogpile).
  - **Continuous sync: outbox-pattern + cron reconciliation.** Periodic drift detection between `events.available_tickets` and `event:{id}:qty`.
  - **Crash recovery: rehydrate from DB, accept seconds of recovery time.** No AOF/RDB needed for this use case.
  - **NOGROUP is an architectural failure signal** — alert + structured log, do NOT silently recreate the group with `$` (loses messages) or `0` (replays everything as duplicates).
  - **CDC / Debezium: overkill** for single-app + single-DB scope. Justified only when 100+ services need a shared cache view.

- **What this finding is NOT.** Not a code-correctness bug under normal production operation (NOGROUP shouldn't fire unless someone manually `FLUSHALL`s or deletes the group). Not justification for B3 inventory sharding (saturation profile [PR #71](https://github.com/Leon180/booking_monitor/pull/71) showed Redis at 53% CPU — well below saturation). Not a Kafka-migration trigger (current scale doesn't warrant it). It IS a missing-design surface around cache vs DB consistency that future operations will need.

- **Sequenced fix plan.**
  1. **PR-A (this one): `make reset-db` precise DEL + backlog entry.** Test environment never again triggers NOGROUP self-heal during active production. Tooling fix only — no code-behavior change. ✓ landed in this PR.
  2. **PR-B: app-startup bulk rehydrate from DB → Redis.** Advisory-lock-guarded scan of `events` table; populate `event:{id}:qty` keys via `SETNX` (don't overwrite live values). Closes the "Redis crash → no inventory state" gap. After this lands, `redis.conf` can flip to `appendonly no` + `save ""` because rehydrate becomes the recovery mechanism.
  3. **PR-C: NOGROUP self-heal upgrade.** Keep self-healing (avoid k8s pod restart cascade) but emit a `consumer_group_recreated_total` metric + WARN log + alert rule. Don't change `$` to `0` — both are wrong. The right answer in a single-instance setup is "alert; operator investigates whether messages were lost; rehydrate from DB if needed".
  4. **PR-D: inventory drift reconciler.** Extend the existing [`recon` subcommand](../internal/application/recon/) to scan `event:{id}:qty` vs `events.available_tickets`, emit `inventory_drift_orders` gauge, optionally repair via `XGROUP SETID` + cache update.

- **Why deferred.** PRs B/C/D each warrant their own scoped review. PR-A unblocks the test environment immediately; B/C/D are sequenced design work that should not be rushed under the same change.

- **Revisit when.** PR-B before any production-shaped deploy. PR-C alongside PR-B (alert hygiene). PR-D when reconciler infrastructure has bandwidth.

- **References (verified 2026-05-03).**
  - Empirical evidence: [`docs/saturation-profile/20260502_221629_c500/`](saturation-profile/20260502_221629_c500/) Findings + audit follow-up
  - Code touchpoints: [`redis_queue.go`](../internal/infrastructure/cache/redis_queue.go) Subscribe loop, [`uow.go`](../internal/infrastructure/persistence/postgres/uow.go) Tx pattern, [`redis.conf`](../deploy/redis/redis.conf) persistence config
  - Industry: Alibaba [Apsara­DB for Redis flash sales](https://www.alibabacloud.com/help/en/redis/use-cases/use-apsaradb-for-redis-to-build-a-business-system-that-can-handle-flash-sales), Stripe [Idempotency keys](https://stripe.com/blog/idempotency), [redis/redis discussions/13680](https://github.com/redis/redis/discussions/13680) (NOGROUP recovery thread), [Dapr components-contrib#3219](https://github.com/dapr/components-contrib/issues/3219) (NOGROUP-after-restart)

### Section-level inventory sharding (Phase 3+, conditional) (2026-05-03)

- **Contract.** Events are coarse-grained containers; each event holds multiple sections (price tiers / seating areas). The real sharding axis for inventory is `(event_id, section_id)`, not `event_id`. Single-key Lua serialisation caps `accepted_bookings/s` per `(event_id, section_id)` pair at ~8,330 — a physical ceiling proven empirically by the VU scaling stress test ([blog post](blog/2026-05-lua-single-thread-ceiling.zh-TW.md) has the full derivation).

- **Two-layer plan** (industry consensus across Alibaba Singles' Day, Ticketmaster Verified Fan, China Railway 12306, JD.com flash sales):
  - **Layer 1 — Section-level sharding (business axis, default for all multi-section events).** Each event × section gets its own Redis key namespace, e.g. `event:{uuid}:section:{section_id}:qty`. Each section has independent sold-out detection — no cross-shard SUM. Schema-level concern only; no extra Redis instances needed at this layer.
  - **Layer 2 — Hot-section quota pre-allocation (infra axis, only for hot sections).** For sections expected to be saturated (Taylor Swift VIP front row, JD.com Singles' Day iPhone), pre-split the quota across N Redis instances, with a router service routing to whichever instance still has free quota. Each shard sees its own sold-out independently; "section completely sold out" is a binary AND across N shards.

- **Why quota pre-allocation ≫ generic hash sub-sharding.** Hash sub-sharding requires SUM across N shards to detect sold-out — real cross-shard consistency cost. Quota pre-allocation lets each shard see its own state — sold-out is per-shard, only "entire section sold out" needs aggregation (and that's a cheap binary AND).

- **Industry references.**
  - Alibaba Singles' Day SKU sharding (hot SKUs pre-allocated quotas across multiple Redis)
  - Ticketmaster Verified Fan (event × ticket type as Layer 1; high-demand events split traffic at Layer 2)
  - China Railway 12306 (train + seat-class as Layer 1; hot trains' hot classes split via "inventory routing table")
  - JD.com flash sale (SKU + hot SKU pre-allocated to N nodes)

- **Trigger conditions (when to do Layer 1).**
  1. Business need surfaces million-ticket-per-event scenarios
  2. Business need shifts to "sub-second × selling out" (vs current "12 seconds is fine")
  3. Multiple concurrent events saturate a single Redis instance's CPU (sustained ~80%)

- **Required preparation in Pattern A (D1-D7).** Add `section_id` to `events` and `orders` schema even though we won't shard yet. This makes Layer 1 a routing change rather than a schema migration. **Low-cost, high future-value design choice — should ship in Phase 3 regardless of sharding decision.**

- **Layer 2 is portfolio-demo grade.** Implementing one hot section's quota router + N Redis instances + a benchmark confirming N× linear scaling is the concrete answer to the standard "how do you handle hot keys" architecture question. Worth doing as a standalone PR after Pattern A and CHANGELOG-driven v0.5.0 / v1.0.0 milestones — **not crammed into the Phase 3 main line**.

- **What this is NOT.** Not a justification for generic hash sharding (the textbook version that nobody uses standalone). Not a justification for Redis Cluster as a first move (cluster mode adds Lua single-slot constraints; only worth it after Layer 1 + Layer 2 saturate). Not a contradiction of the saturation-profile finding that single-instance Redis is the right choice for our current scale ([PR #71](https://github.com/Leon180/booking_monitor/pull/71)).

- **Related docs.**
  - Derivation: [`docs/blog/2026-05-lua-single-thread-ceiling.zh-TW.md`](blog/2026-05-lua-single-thread-ceiling.zh-TW.md)
  - Empirical evidence: [`docs/saturation-profile/`](saturation-profile/), [`docs/benchmarks/20260502_132335_compare_c500_vu_scaling/`](benchmarks/)
  - Cache-truth contract this builds on: [Cache-truth architecture entry](#cache-truth-architecture-redis-as-ephemeral-db-as-source-of-truth-2026-05-03)

### KKTIX-aligned ticket type model (Phase 3+, committed) (2026-05-04)

- **Commitment.** This project explicitly aligns its inventory + pricing model to **KKTIX-shape ticket types**, NOT generic e-commerce SKU or Pretix Item/Quota M:N. The unit of inventory + pricing + sale-rules is `event_ticket_type`. Section / area is **optional metadata** on a ticket type (`area_label`), not an independent dimension. Decided 2026-05-04 after comparative research against Pretix (open-source code), Stripe Product/Price, IATA NDC fare class, and KKTIX user-facing observation.

- **What this means concretely.**
  - Each event has 1..N ticket types. Examples: "VIP A 區早鳥票", "A 區一般票", "學生票C區", "套票(VIP+T恤)"
  - Ticket type holds: name, price_cents, currency, total / available, sale_starts_at / sale_ends_at, per_user_limit, area_label?
  - Order references `ticket_type_id` (not `event_id` directly, not `section_id`)
  - Order snapshots `amount_cents` + `currency` at book time (industry SOP — see [`docs/design/ticket_pricing.md`](design/ticket_pricing.md))
  - Hold = Order in `awaiting_payment` status (no separate cart table — see [`docs/design/inventory_hold.md`](design/inventory_hold.md))

- **Why this alignment.** KKTIX is the local-market reference (TW interview pool weights it heavily). Their model is also closest to what Eventbrite / Ticketmaster / 大型演唱會 actually use, even if their internal schema isn't public. Pretix's M:N Item/Quota is more academic-correct but more ceremony than our scope needs. Stripe's Product/Price is a generic e-commerce model that doesn't capture sale-window / per-user-limit / area metadata that ticketing needs.

- **Supersedes the section-level entry above** for terminology only — the two-layer sharding plan still applies, just rename `section` → `ticket_type` throughout. The Layer 1 / Layer 2 sharding axis becomes `ticket_type_id` (not `section_id`). Sharding's underlying logic is unchanged.

- **Concrete next steps.**
  - **D4.1 — Ticket type model + price snapshot** (between D4 and D5 in roadmap): rename `event_sections` → `event_ticket_types`; add price_cents / currency / sale_window / per_user_limit / area_label; orders.amount_cents snapshot at book time
  - **D8 (revised)** — adds `seats` table referencing `ticket_type_id`, NOT a separate "section" entity. Section becomes `ticket_types.area_label` string metadata
  - **D9-minimal** benchmarks Pattern A two-step flow with the new ticket_type model

- **Empirical decisions deferred to benchmark.**
  - Single-table (ticket_type holds price + inventory + area metadata) vs two-table (section + ticket_type independent) → benchmark before deciding. p95 noise floor < 5% threshold for keeping single-table; > 10% threshold for splitting. See [`docs/design/ticket_pricing.md` §5](design/ticket_pricing.md)

- **What this is NOT.**
  - Not a commitment to copy KKTIX's internal schema (we don't have it)
  - Not a commitment to ship every KKTIX feature (early bird / 學生票 are schema-supported but business-rule-out-of-scope until D8+)
  - Not a precommitment to Pretix's M:N Item/Quota — that's marked as future expansion in [`docs/design/ticket_pricing.md` §8](design/ticket_pricing.md), trigger condition documented

- **Related design docs.**
  - [`docs/design/ticket_pricing.md`](design/ticket_pricing.md) — pricing + ticket type schema research
  - [`docs/design/inventory_hold.md`](design/inventory_hold.md) — order = hold pattern decision

---

## Testing

### Real rollback-failure unit test path
- **What.** `RecordRollbackFailure` metric fires when `tx.Rollback()` returns a non-`ErrTxDone` error. Currently uncovered by any test (the application-layer `runUowThrough` simulator can't produce a real rollback; the integration suite uses real Postgres which doesn't naturally fail Rollback).
- **Why deferred.** Reproducing requires a custom `database/sql/driver` shim that fault-injects on Rollback. Possible but disproportionate work for a single metric path that's already a "should never fire in production" signal.
- **Revisit when.** A real rollback-failure incident produces a metric value > 0 — the absence of test coverage will then matter for a fix-confirmation PR.

### `TestMain` shared-container refactor for integration suite
- **What.** `test/integration/postgres/` currently boots one container per test (~1s overhead per test). At 40 tests / ~38s total this is acceptable; past ~50 tests the per-test cost dominates.
- **Why deferred.** Per-test isolation is the safer pattern; sharing requires careful Reset discipline. Not worth the refactor cost yet.
- **Revisit when.** Per-test runtime exceeds 30s OR total exceeds 3 minutes. CI workflow comment in `.github/workflows/ci.yml` records the threshold.

### Mock gateway deterministic failure mode
- **What.** `MockPaymentGateway` currently has a hardcoded 95% failure rate. Saga / E2E tests are statistical rather than deterministic.
- **Why deferred.** Statistical testing catches a different class of bug (race-condition variability under realistic flake). Deterministic mode would be cleaner for unit-style assertions.
- **Revisit when.** A specific saga-path test needs deterministic behavior to assert a single outcome.

---

## API surface

### `GET /api/v1/events/:id` full implementation
- **What.** Currently a stub that returns `{"message": "View event", "event_id": ...}` + bumps `page_views_total`. Doesn't load event details from `EventRepository`.
- **Why deferred.** Implementing requires touching application/domain/handler layers; the demo (Phase 3) is the natural time when "view event" becomes a real user flow.
- **Revisit when.** Phase 3 D-series PRs land — specifically D8 (frontend bootstrap) which will exercise the read path.

### Auth / RBAC + ownership scoping
- **What.** Currently any client can call any endpoint. Idempotency keys are global (not user-scoped).
- **Why deferred.** Roadmap N9 covers this; explicit pre-auth framing for the simulator is honest.
- **Revisit when.** N9 or first external consumer.

---

## Operational follow-ups (sourced from concrete events)

### DLQ-12K-entries triage
- **What.** During PR #66's smoke test, `OrdersDLQNonEmpty` was firing with 12092 entries — these had been accumulating silently under the prior null receiver. Documented in [`docs/checkpoints/20260501-senior-multi-agent-review.md`](checkpoints/20260501-senior-multi-agent-review.md) Outcome.
- **Why deferred.** PR #66 was scoped to alert delivery; triaging actual DLQ contents is operational work, not infrastructure.
- **Revisit when.** Next operational sweep. Determine: stress-test artifact, stuck consumer, or payment-worker failure mode? Drain or investigate.

<!-- Reconciler max-age force-fail outbox emit: VERIFIED FIXED (PR #51 / CP1).
     `reconciler.failOrder()` runs MarkFailed + outbox.Create inside a single
     UoW.Do, so the saga compensator is correctly triggered on force-fail.
     Removed from this backlog 2026-05-02 per the close-when-done rule. -->


---

## How to use this file

When a PR reviewer or design discussion surfaces "we should do X but not now", capture it here with the same shape (**what / why / revisit when**) before closing the conversation. The file is the single place to look when the question is "is this a known thing we already chose to defer?".

When an item is closed (acted on, or no longer relevant), remove it from this file with a brief commit note pointing at the resolving PR. Don't keep stale entries — the value of a backlog is that it's small enough to read in full.
