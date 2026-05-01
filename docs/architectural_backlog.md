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

### Reconciler max-age force-fail outbox emit
- **What.** A4 reconciler force-fail path was found to miss the outbox emit (so saga compensator never triggers). Recorded in `docs/checkpoints/20260430-phase2-review.md` as DEF-CRIT.
- **Why deferred.** Cleanup PR scope was limited; the fix is a real domain change.
- **Revisit when.** Next reliability-arc PR (CP1 era). May already be addressed — verify before treating as open.

---

## How to use this file

When a PR reviewer or design discussion surfaces "we should do X but not now", capture it here with the same shape (**what / why / revisit when**) before closing the conversation. The file is the single place to look when the question is "is this a known thing we already chose to defer?".

When an item is closed (acted on, or no longer relevant), remove it from this file with a brief commit note pointing at the resolving PR. Don't keep stale entries — the value of a backlog is that it's small enough to read in full.
