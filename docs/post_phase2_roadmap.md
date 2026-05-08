# Post-Phase-2 Roadmap

Forward-looking sprint plan, written 2026-04-30 immediately after the Phase 2 checkpoint review. Supersedes prior memory-only roadmaps for sequencing decisions. Historical architecture evolution lives in [`scaling_roadmap.md`](scaling_roadmap.md) and is not duplicated here.

## Where we are

- **Phase 2 done**: PRs #45 (charging two-phase intent log + reconciler) → #46 (streams obs + DLQ MINID) → #47 (response shape + GET /orders/:id) → #48 (N4 idempotency fingerprint) → #49 (A5 saga watchdog + checkpoint framework).
- **First project-review checkpoint completed**: [`docs/checkpoints/20260430-phase2-review.md`](checkpoints/20260430-phase2-review.md). Grade A−. Findings split between a focused cleanup PR (Critical + cheap-Important) and 8 deferred follow-up PRs.
- **`v0.5.0` shipped 2026-05-07** — Pattern A D1–D6 complete: reservation + payment intent + webhook + expiry sweeper. End-to-end flow `book → /pay → confirm OR expire → paid OR compensated` runs against the Docker stack. See [CHANGELOG.md §0.5.0](../CHANGELOG.md) for the per-PR ledger.
- **D7 + D8-minimal both shipped 2026-05-08** — D7 deleted the legacy A4 auto-charge path (payment_worker binary + `order.created` consumer + `ProcessOrder`); saga scope narrowed to `{expired, payment_failed}`. D8-minimal browser demo (Vite + React + TS in `demo/`) ships against the CORS allow-list from PR #96.
- **Next user goal**: D12 4-version multi-cmd comparison harness (~4 wk; the senior-interview centerpiece) and/or D14+D16 portfolio narrative quick-wins (~1-2 days each). D4.2 (real Stripe SDK adapter) is parallel-safe with any of the above.

## Sequence

```
Phase 2.5 (cleanup, ~1 wk)
  CP1  cleanup PR (rows 1–9 from action plan)
  CP2  recon Config + Metrics interfaces + saga.Config (architecture row 10)
  CP2.5 application-port relocations: move `DistributedLock` and `EventPublisher` interfaces from `internal/domain/` to `internal/application/` (their `_only_ consumer is OutboxRelay; both have no domain invariants — pure plumbing ports). `IdempotencyResult` + `IdempotencyRepository` stay in domain (borderline; HTTP-transport semantics are defensible as API-protocol contracts). Wire-format constants like `EventTypeOrderCreated` stay in domain per coding-style rule 5. Small follow-up to CP2 — same architectural-cleanup theme, ~30 LOC + import updates across consumers.
  CP2.6 `internal/application/` subpackage tidy: the project started subpackaging (`payment/`, `recon/`, `saga/`) in earlier PRs but stopped halfway — booking, worker, and outbox flows still live as ~16 flat files at the top level. Split into reviewable chunks during execution: **CP2.6a** = booking/ subpackage + BookingService Order alignment (the only step with semantic change); **CP2.6b** = mechanical file moves (worker/, outbox/, event/, saga compensator, payment shim, db_metrics relocate). Promote each cohesive flow to its own subpackage:
    - new `application/booking/` (booking_service.go + service_metrics.go + service_tracing.go + booking_metrics.go + tests)
    - new `application/worker/` (worker_service.go + worker_metrics.go + queue.go + queue_metrics.go + queue_policy.go + message_processor.go + message_processor_metrics.go + tests)
    - new `application/outbox/` (outbox_relay.go + outbox_relay_tracing.go + tests)
    - move `saga_compensator.go` → `application/saga/compensator.go` (flat-namespace remnant from before `saga/` existed)
    - move `db_metrics.go` → `internal/infrastructure/observability/` (it's an adapter wrapping `sql.DB.Stats()`; misplaced at the application layer)
    - stays flat: `module.go` (top-level fx), `uow.go` (cross-package UnitOfWork), `order_events.go` (wire-format DTOs consumed by payment/saga/recon/worker)
    - `event_service.go`: subpackage `application/event/` for symmetry, OR keep flat (1 file; borderline). Decide during execution.
    - `payment_service.go` (top-level interface shim): fold into `application/payment/` near its impl, or rename `payment_port.go` and keep flat. Decide during execution.

   **Bundled alignment fix** (closes "BookingService doesn't construct a domain.Order" misalignment surfaced during the CP2.5 review): change `BookTicket` to call `domain.NewOrder(orderID, userID, eventID, quantity)` BEFORE the Redis deduct so invariant validation runs at the application boundary instead of being deferred to the worker (where the codebase's own [`queue.go:15-20`](../internal/application/queue.go) doc admits *"NO business invariants enforced; NewOrder downstream does"*). Return `domain.Order` instead of bare `uuid.UUID` so the signature is symmetric with `GetOrder`. Worker's `domain.NewOrder` becomes defense-in-depth (same code, no duplication risk). Stream payload unchanged. ~30 LOC + handler/test updates. Closes the violation of coding-style rule 1 ("Construct via factory... so invariant validation lives in one place").

   ~16 file moves + ~15-20 import updates across cmd files + tests + the BookTicket alignment fix. Pure refactor — no behaviour change for valid traffic; invalid traffic now rejected at the application boundary instead of producing a Redis deduct → worker DLQ → revert sequence. Lands BEFORE CP3 so the metrics-file split has fewer cross-package imports to coordinate, and BEFORE Phase 3 so Pattern A's reservation flow extends `application/booking/` cleanly rather than further crowding the flat namespace.
  CP3  observability/metrics.go split + middleware move (row 11). Split into a/b — same playbook as CP2.6:
    **CP3a** (semantic): move `MetricsMiddleware` + its `httpRequestsTotal` + `httpRequestDuration` vars from `internal/infrastructure/observability/` to `internal/infrastructure/api/middleware/`. Removes the `gin` import from observability — that was the layer-classification gap the checkpoint A2 actually flagged. Rename `MetricsMiddleware` → `middleware.Metrics` (drop redundant suffix). Single consumer update in `cmd/booking-cli/server.go`. Metric names unchanged so no `docs/monitoring.md` update needed. ~3 files, ~50 LOC.
    **CP3b** (mechanical): split the remaining ~558-line `observability/metrics.go` into per-concern files: `metrics_booking.go` (BookingsTotal, PageViewsTotal), `metrics_worker.go` (WorkerOrdersTotal + the worker-side counters), `metrics_idempotency.go` (Idempotency*Total + Cache*Total), `metrics_redis_streams.go` (Redis XAck/XAdd/Revert/DLQ + collector counters), `metrics_db.go` (DBRollbackFailuresTotal), `metrics_recon.go` (already grouped), `metrics_saga.go` (already grouped). Pure organization — no architectural risk.

    **CP3b implementation guidance: keep ONE central `init()`** in a coordinator file (rename `metrics.go` → `metrics_init.go` or similar), split only the `var X = promauto.NewX(...)` declarations across files. Reason: Go spec says multiple `init()` in the same package run in "presentation order" which is implementation-defined (alphabetical via `gc` today, but a spec-level guarantee). Splitting the init() across files makes a future "this metric assumes that one is registered first" assumption silently breakable depending on file-name sort. Label pre-warming is idempotent so this isn't urgent today, but the centralized init() preserves the cosmetic-organization win without introducing implementation-defined ordering.

    **Known limitation NOT addressed by CP3**: `promauto` registers into `prometheus.DefaultRegisterer` (process-global). This drove CP1's parallel-counter race; CP2 fixed the symptom with `recordingMetrics` fakes in the application layer, but the underlying coupling is still there for any future test that reads globals. The "fully fix" path is replacing package globals with a `Registry` struct injected via fx (every metric-emitter takes `*Metrics` as a parameter; tests get their own). Cost is moderate, benefit is true test isolation. Not worth doing as part of CP3 — track in `architectural_backlog.md` for "if we ever hit a CI flake the application-layer fakes don't catch."
  CP4  testcontainers integration suite for repositories.go (row 12 = roadmap N8) — split into 4a/4b/4c.
    **CP4a (DONE in PR #63, 2026-05-01)** — testcontainers-go harness + OrderRepository integration suite. New `test/integration/postgres/` package: `harness.go` boots postgres:15-alpine, applies all migrations via plain SQL exec (no `migrate` CLI dependency), exposes Reset + SeedEvent helpers. 11 OrderRepository integration tests pinning what row-mapper unit tests can't reach: CTE-based atomic state transitions (the audit-log INSERT atomicity in `transitionStatus`), the `uq_orders_user_event` partial unique index (allows re-buy after Failed), the `EXTRACT(EPOCH FROM ...)` time arithmetic in FindStuckCharging/Failed, and the partial-index-driven query plans from migrations 000010/000011. Build-tagged `integration` so `go test ./internal/...` stays container-free; new `make test-integration` + CI `test-integration` job opt in. Suite runs in ~14s on local docker (1 container per test for isolation; TestMain-based sharing was deliberately rejected to avoid ordering coupling — revisit if suite grows past 50 tests).
    **CP4b (DONE in PR #64, 2026-05-01)** — EventRepository + OutboxRepository + PostgresUnitOfWork integration suite. Reused the CP4a harness, added `SeedOrder` helper. 21 new tests (10 EventRepository + 6 OutboxRepository + 5 UoW). Closes silent-failure-hunter Finding 3 from CP7 (the application-layer `runUowThrough` simulator can't verify rollback semantics — this suite does). Specifically pins:
    - DecrementTicket's oversell-prevention WHERE predicate + ErrSoldOut sentinel mapping (boundary case at exact-available-count covered)
    - IncrementTicket's over-restore-prevention WHERE predicate (saga compensation safety: duplicate compensation events MUST NOT push available_tickets above total_tickets)
    - Update's narrow scope (only available_tickets, not name) — pins current contract so a future widening fails the test deliberately
    - OutboxRepository's `WHERE processed_at IS NULL` filter (without it, relay would re-publish on every tick), the `ORDER BY id ASC` causal ordering, MarkProcessed idempotency under at-least-once delivery
    - PostgresUnitOfWork commit-on-nil + rollback-on-err atomicity (the load-bearing saga + outbox guarantee), fresh Repositories bundle per Do invocation (Morrison foot-gun protection), BeginTx error propagation
    Total integration suite is now 38 tests in ~38s; well within CI's 5min timeout.
    **CP4c (DONE in PR #65, 2026-05-01)** — migration round-trip test. Single shared container, walks all 11 migrations through `up → down → up`, asserts schema-state equality at each step (tables, columns, indexes including partial-WHERE clauses). The test surfaced a real latent bug: `000003_add_outbox.down.sql` had `DROP TABLE IF NOT EXISTS events_outbox` — invalid SQL syntax (Postgres only supports `IF EXISTS` on DROP). Bug had been latent since the migration was written because no one had ever actually run that down migration. Fixed in this PR. Plus a `TestMigrationsAllPaired` guard so a future migration without a paired down.sql fails CI loudly rather than slipping through. Total integration suite is now 40 tests in ~38s (the round-trip is the cheap part — single container).
  CP5  docs/runbooks/* + alerts runbook_url annotations (row 13 = roadmap N3) — DONE in PR #61 (2026-05-01). Single consolidated [`docs/runbooks/README.md`](runbooks/README.md) (Stripe / Lyft style — one file, one anchor per alert; easier to keep in sync than 24 separate docs that drift independently). All 24 alerts in `deploy/prometheus/alerts.yml` now carry a `runbook_url` annotation pointing at the matching anchor. Bundled with CP6 since runbook + delivery are load-bearing together.
  CP6  Alertmanager deployment + delivery target wiring (row 14 = roadmap N3) — DONE in PR #61 (2026-05-01). Added `booking_alertmanager` service (prom/alertmanager:v0.27.0) on port 9093, wired Prometheus → Alertmanager via the new `alerting:` block in `prometheus.yml`. Default config uses a `null` receiver so the dev stack works without a real Slack webhook; opt-in via `deploy/alertmanager/alertmanager.slack.yml.example`. Severity-specific cadences (critical 30 m repeat / warning 4 h / info 24 h), grouping by `alertname + severity`, and inhibition (`RedisStreamCollectorDown` suppresses other stream-backlog alerts). Smoke-tested: Prometheus reports Alertmanager active in `/api/v1/alertmanagers`; runbook_url annotations are present on loaded rules.
  CP7  test-surface sprint: saga_compensator, handler coverage, outbox.Run (row 16) — DONE in PR #62 (2026-05-01). 4 new test files / 23 new test cases:
    - `internal/application/saga/compensator_test.go` (7 cases) — covers unmarshal failure / already-compensated short-circuit / GetByID error / IncrementTicket error / MarkCompensated rollback / RevertInventory failure / happy path. saga package: 95.1% coverage.
    - `internal/application/event/service_test.go` (6 cases) — covers domain invariant rejections / DB Create error / Redis SetInventory failure with successful Delete-back / dangling-row case (both errs surfaced) / happy path. event package: 100.0% coverage.
    - `internal/application/outbox/relay_test.go` extended (5 new Run-loop cases) — ctx-cancelled-before-tick / TryLock error / standby replica / leader runs batch + Unlocks on exit / batchFn error continues loop. Closes the Run-loop branches the prior `processBatch`-only test couldn't reach. outbox package: 73.5% coverage.
    - `internal/infrastructure/api/booking/handler_test.go` extended (5 new cases) — HandleCreateEvent (happy / invalid body / service error with no driver-text leak) + HandleViewEvent + HandleListBookings (defaults applied / status filter parsed). booking handler package: 54.6% coverage.
  CP8  header-bearing N4 benchmark (row 17) — DONE in PR #59 (2026-05-01). See [`docs/benchmarks/20260501_175422_compare_c500/comparison.md`](benchmarks/20260501_175422_compare_c500/comparison.md). Result: N4 fingerprint path costs ≈18% RPS / ≈46% p95 vs no-header baseline at 500 VUs (cold-path: SETNX + SHA-256 fingerprint + payload-write — two extra Redis round-trips dominate). Materially higher than the pre-N4 estimate of "<2% RPS / 2-5% p95"; calibration captured for capacity planning. Bundled with the k6_comparison.js 200→202 contract fix surfaced by the prior CP3b no-regression run.
  CP9  Grafana dashboard panels for recon / saga / DLQ / DB pool / cache (row 18) — DONE in PR #60 (2026-05-01). 6 collapsible row sections + 18 new panels added to `deploy/grafana/provisioning/dashboards/dashboard.json` covering A4 recon, A5 saga watchdog, Kafka+Redis DLQ activity, PG pool USE method, idempotency cache hit-rate / errors / replay outcomes, and Redis stream/DLQ infra failures. Queries mirror the alert exprs in `deploy/prometheus/alerts.yml` so an alert that fires has a matching panel for triage. `docs/monitoring.md` + zh-TW §4 updated with the full panel inventory.

Phase 3 (demo readiness, ~5–7 wk — TRIMMED for portfolio focus) — Pattern A + 4-version comparison + minimal frontend

> **Scope decision (2026-05-03):** trimmed from the original ~9–13 wk plan. Senior-engineer interviews almost never click through to a polished demo UI — they read README + PR history + benchmark archive + maybe a short asciinema. Frontend mission-control SSE (D11) and the React frontend comparison view (D13) cost 7-10 weeks for ~5% interview-impact value, so they were cut. The minimal Stripe Elements page (D8-minimal, replacing original D8) stays because Taiwan-local interviews (KKTIX / Pinkoi / 91APP / 街口) DO weight portfolio sites slightly higher, and "let a non-engineer click through Stripe checkout" is the threshold for that. D9 (k6 scenario for two-step flow) and D10 (demo recording) are kept but trimmed: terminal-recorded asciinema replaces the original "5-minute video with mission-control overlay" scope.
>
> **Replaced with:** D14 README architecture diagram (mermaid, ~1 day), D15 multi-post engineering blog series under `docs/blog/` using a hybrid-STAR template (~3-5 days, paced incrementally), D16 CHANGELOG.md + retroactive git tags + GitHub Releases page (~1 day, highest ROI of the three because it surfaces architecture-evolution at the GitHub repo entry). Asciinema terminal walkthrough lands as D10-minimal under "Minimal demo polish" rather than the portfolio-narrative section. Total replacement cost ~5-7 days vs ~7-10 weeks original.

  3a. Pattern A core (~3–5 wk) — UNCHANGED, this is the real architectural learning
  D1   schema migration: add orders.reserved_until + orders.payment_intent_id; new status `awaiting_payment`
  D2   domain state machine: pending → awaiting_payment → paid | expired | failed
  D3   POST /api/v1/book becomes a reservation (TTL 10–15 min); response includes payment_intent metadata
  D4   POST /api/v1/orders/:id/pay creates the PaymentIntent against the gateway adapter (Stripe-like)
  D4.1 **KKTIX ticket type model + price snapshot** (added 2026-05-04, between D4 and D5)
       — rename `event_sections` → `event_ticket_types`; add `price_cents`, `currency`,
         `sale_starts_at`, `sale_ends_at`, `per_user_limit`, `area_label`
       — `orders.section_id` → `orders.ticket_type_id`; orders gain `amount_cents` + `currency` snapshot at book time
       — BookingService.BookTicket reads price from ticket_type, snapshots onto order
       — payment.Service.CreatePaymentIntent reads `order.amount_cents` (not from config / not re-querying ticket_type)
       — removes `BookingConfig.DefaultTicketPriceCents` / `DefaultCurrency` (architectural smell)
       — Decision rationale + alternatives considered: see [`docs/design/ticket_pricing.md`](design/ticket_pricing.md) and [`docs/architectural_backlog.md` § KKTIX-aligned ticket type model](architectural_backlog.md)
       — Empirical decisions inside (single-table vs two-table) gated on benchmark, not speculation
  D4.2 **StripeGateway adapter** (post-D4.1; can run in parallel with D5)
       — `internal/infrastructure/payment/stripe_gateway.go` against real Stripe test API (`sk_test_...`)
       — env var `PAYMENT_GATEWAY_MODE=mock|stripe_test|stripe` switches fx-provided adapter
       — `docker-compose.test.yml` adds `stripe-mock` service for offline CI
       — application layer NOT modified — proves Clean Architecture's gateway swap-in/swap-out
  D5   ✅ POST /webhook/payment shipped (PR #92; merged 2026-05-06). Stripe-shape signature verification (HMAC-SHA256 over `t.<body>` with 5-min skew tolerance), order resolution via metadata.order_id primary + payment_intent_id fallback (partial unique idx in migration 000015). Race-aware MarkPaid SQL guards `reserved_until > NOW()` so a late-success webhook routes to the expired path + saga refund instead of soft-locking inventory. SetPaymentIntentID widened to 3-sentinel disambiguation (`ErrOrderNotFound` / `ErrReservationExpired` / `ErrInvalidTransition`) so D4 + D5 share the same race-classification contract. Mock confirm gated by `ENABLE_TEST_ENDPOINTS` (off in prod) — preserves D4 client-confirm contract + lets D6 expiry demo work.
       — `payment_intent.succeeded` → MarkPaid (orphan-repair: same UoW also persists `payment_intent_id` if the SetPaymentIntentID race lost it earlier), `payment_intent.payment_failed` → MarkPaymentFailed + emit `order.failed` (saga compensator)
       — 4 alerts: `PaymentWebhookSignatureFailing` (warning), `PaymentWebhookUnknownIntentSurging` (critical, orphan rescue), `PaymentWebhookLateSuccessAfterExpiry` (single-event paging, manual refund), `PaymentWebhookIntentMismatch` (single-event paging, possible forgery)
       — assumes D4.2 NOT a hard prerequisite: D5 ships with mock provider; real Stripe SDK adapter still queued as D4.2 follow-up
  D6   ✅ reservation expiry sweeper shipped (PR #93; merged 2026-05-07). `booking-cli expiry-sweeper` subcommand (loop + `--once` modes), structurally a near-clone of A5 saga-watchdog. DB-NOW SQL (`reserved_until <= NOW() - $1::interval`) shares time source with D5's `MarkPaid` predicate — no app-clock skew can produce premature expiry. **MaxAge does NOT gate the transition** — D6 always expires, MaxAge is observability-only via the `expired_overaged` outcome label + dedicated `expiry_max_age_total` counter; saga compensator's `saga:reverted:order:<id>` SETNX makes the re-emit idempotent. Per-row `MarkExpired + emit order.failed` in one UoW; saga compensator (separate process) consumes `order.failed` and reverts Redis via `revert.lua`. D6 itself never touches Redis (layering: D6 owns timing, saga owns inventory revert).
       — observability: 5 metrics (`expiry_sweep_resolved_total{outcome}` with 7 outcomes, `expiry_max_age_total`, `expiry_find_expired_errors_total`, `expiry_oldest_overdue_age_seconds`, `expiry_backlog_after_sweep`) + 2 histograms (per-row `expiry_resolve_duration_seconds` and full-sweep `expiry_sweep_duration_seconds`)
       — 4 alerts: `ExpiryOldestOverdueAge` (warning, 5m soak), `ExpiryProcessingErrors` (warning, per-row resolve failure rate), `ExpiryFindErrors` (critical, DB blind — covers find AND count query failures), `ExpiryMaxAgeExceeded` (critical, single-event paging — informational; D6 already expired the row, alert asks operators to investigate why it sat past 24h)
       — concurrent webhook race verified at 3 levels (plan v4 §E + repo race test): D6-wins / D5-payment_failed-wins / future-reservation-not-eligible. Net: at most one `order.failed` per order; saga SETNX is the second-line guard
       — closes the v0.5.0 demo arc: book → never confirm → reservation expires → saga reverts → ticket inventory restored, all in ~62s with `BOOKING_RESERVATION_WINDOW=20s` smoke override
  D7   ✅ saga scope narrowed (PR pending merge; branch `feat/d7-narrow-saga-scope`, 2026-05-08). Deleted the legacy A4 auto-charge path: `payment.Service.ProcessOrder`, `messaging/kafka_consumer.go` (the `order.created` consumer), `cmd/booking-cli/payment.go`, the `payment_worker` docker-compose service, the `payment-worker` Prometheus scrape job, `domain.PaymentCharger` + `PaymentGateway.Charge` + `MockGateway.Charge`, `domain.EventTypeOrderCreated` + `NewOrderCreatedOutbox`, `application.OrderCreatedEvent` + `NewOrderCreatedEvent` + `NewOrderFailedEvent(from OrderCreatedEvent)`, `KafkaConfig.PaymentGroupID` + `OrderCreatedTopic`. Saga compensator's `order.failed` topic now has only D5 webhook (`payment_failed`) and D6 expiry sweeper (`expired`) as production emitters; recon's `failOrder` is a third (rare) emitter for stuck-charging force-fails. Saga consumer was always in-process inside `app`. `payment.Service` interface narrowed to `CreatePaymentIntent` only; `payment.NewService` parameter narrowed from `domain.PaymentGateway` to `domain.PaymentIntentCreator` (fx provider advertises that narrow type via `fx.As`).
       — bilingual docs updated (README, PROJECT_SPEC, AGENTS, CLAUDE, monitoring) with a top-of-section D7 deprecation callout in PROJECT_SPEC §3.5; the historical A4 happy-path table rows are kept as architectural-evolution context. Active rules docs (`.claude/rules/golang/{patterns,coding-style}.md`) updated to reference `EventTypeOrderFailed`/`NewOrderFailedOutbox` instead of the deleted `EventTypeOrderCreated`/`NewOrderCreatedOutbox`. Runbooks added a D7 cutover note (drain pending `events_outbox.event_type='order.created'` before deploy).
       — `kafka_consumer_retry_total` metric label set narrowed from `{topic=order.created|order.failed}` to `{topic=order.failed}` only; `KafkaConsumerStuck` alert continues to work; `OutboxPendingBacklog` alert description updated.
       — hot-path benchmark report at `docs/benchmarks/20260508_compare_c500_d7/`: D7's `events_outbox(order.created)` write removal from `MessageProcessor.Process` is non-regressive (`accepted_bookings/s` flat at +0.01%; booking p95 -8.7%; http p95 -9.7%; `http_reqs/s` -1.42% within noise). All within ≤5% gate.

  3b. Minimal demo polish (~1 wk total — replaces original 6–8 wk plan)
  D8-minimal   Minimal Next.js page in `web/` workspace: Stripe Elements payment form + reservation status / countdown display, mounted with `client_secret` from D4. NO admin dashboard, NO React Flow pipeline animation, NO comparison charts. Single-page, demo-purpose only. Uses MockStripeAdapter so demo runs without Stripe credentials. (~3-5 days)
  D9-minimal   k6 scenario script for the two-step reservation+payment flow; one baseline capture under `docs/benchmarks/`. Skip the multi-stage comparison runs that the original D9 implied — those land in D12. (~1 day)
  D10-minimal  Asciinema terminal recording (~2-3 minutes): `make demo-up` → curl reservation → curl payment → curl `/api/v1/orders/:id` showing the state transitions. Plus an embedded link in README. NO video editing, NO mission-control overlay. (~1 day)

  D12  4-version multi-cmd comparison harness (~4 wk) — UNCHANGED, this is THE senior-interview talking point
       Same `internal/` packages, different fx wirings under separate `cmd/` entries — Clean Architecture as the answer:
       - `cmd/booking-cli-stage1/` — API → Postgres `SELECT FOR UPDATE`. No Redis, no Kafka, no async, no saga. Pure synchronous baseline. Estimated 30 LOC main.go.
       - `cmd/booking-cli-stage2/` — API → Redis Lua atomic deduct → SYNCHRONOUS DB write. Inventory in Redis but no async buffering.
       - `cmd/booking-cli-stage3/` — API → Redis Lua → `orders:stream` → worker → DB. Async + worker pool, but no event-driven downstream (no Kafka outbox, no payment service, no saga).
       - `cmd/booking-cli-stage4/` — current `cmd/booking-cli/` as canonical Stage 4. No rename; just add the version label.
       Each binary registers Prometheus default labels with constant `version_tag={stage1..stage4}` so Grafana can split by version. Each implements only `POST /api/v1/book` for the comparison; full feature set stays on Stage 4. `docker-compose` gets a `comparison` profile spinning up all four on ports 8081–8084. Shared Postgres + Redis instances with namespace isolation (per-stage Postgres database, per-stage Redis DB index). k6 takes `--target=http://localhost:8081` argument; results stored under `docs/benchmarks/comparisons/<timestamp>/{stage1,stage2,stage3,stage4}/`.
       **Output: a markdown comparison report (`docs/benchmarks/comparisons/<ts>/comparison.md`) with a table + collapsed plots — NOT a React frontend.** The Recharts visualization originally in D13 is dropped; the markdown comparison stands on its own as interview material.

  3c. Portfolio narrative (~5–7 days; runs in parallel with D8-D10 minimal)
  D14  README architecture diagram — mermaid sequence diagram(s) showing: (a) the four-stage architecture evolution (Stage 1 → Stage 4), and (b) the Pattern A reservation→payment→webhook flow with saga compensation. Embed in main README + cross-link from PROJECT_SPEC. (~1 day)

  D15  Engineering blog series under `docs/blog/` — multi-post format using a hybrid-STAR template (decision made 2026-05-03 after reviewing pure STAR vs free-form options).
       **Why in-repo `docs/blog/` and NOT external (Medium / Hashnode):** posts can reference specific commit/PR SHAs without context-switching, GitHub renders mermaid + code blocks natively, no separate site to maintain. Reviewers can prose↔code in one tab.
       **Why hybrid-STAR (not pure STAR):** pure STAR is interview-prep mechanical; engineering decisions need quantitative evidence + trade-off detail that doesn't fit in STAR's "Result" cell. The hybrid keeps STAR's interview-answer structure while giving each section enough room for benchmark tables, code excerpts, and citations.
       **Per-post template** (each post = one architectural decision):
       ```
       # Title — concrete technical decision (e.g., "Why Redis is ephemeral, not durable")
       ## Context              ← Situation + Task merged. Quantified if possible.
       ## Options Considered   ← 2-4 alternatives, each with 1-2 sentence trade-off.
       ## Decision             ← The chosen option + rationale + PR/commit references.
       ## Result               ← Benchmark numbers, second-order effects, industry citations.
       ## Lessons              ← Honest hindsight. Senior interviews weight this heavily.
       ```
       **First batch (~3-5 posts, written incrementally) — status as of 2026-05-09:**
       - ✅ **Cache-truth architecture: why Redis is ephemeral, not durable** — published 2026-05-03 as [`2026-05-cache-truth-architecture.zh-TW.md`](blog/2026-05-cache-truth-architecture.zh-TW.md) (pairs with v0.4.0)
       - ✅ **The Lua single-thread ceiling: 8,330 acc/s and how to think about the next 10×** — published 2026-05-03 as [`2026-05-lua-single-thread-ceiling.zh-TW.md`](blog/2026-05-lua-single-thread-ceiling.zh-TW.md)
       - ✅ **Recon + drift detection: building the safety net after the silent-loss incident** — published 2026-05-03 as [`2026-05-detect-but-dont-fix.zh-TW.md`](blog/2026-05-detect-but-dont-fix.zh-TW.md) (slightly broader scope: the 4-layer detect-but-don't-fix design rule)
       - 🕐 **Why Docker Desktop on Mac caps your benchmark at ~80k req/s** — data-gated; write AFTER O3.2 variant B benchmark has actual data. The only remaining first-batch item.
       - ✅ **Saga compensation in production-shape Go: outbox + watchdog + idempotency** — published 2026-05-09 as [`2026-05-saga-pure-forward-recovery.zh-TW.md`](blog/2026-05-saga-pure-forward-recovery.zh-TW.md) (reframed during writing: "Saga shouldn't manage the happy path — D7 narrowing as Garcia-Molina 1987 §5's engineering implementation")
       Target was ~1000-1500 words per post, 1-2 days each. **Pace target met 2026-05-03** (first 3 posts before Phase 3 finishes); the saga forward-recovery post (2026-05-09) is the first post-roadmap addition. Authoritative blog index: [`docs/blog/README.md`](blog/README.md).

  D16  Repo storytelling — `CHANGELOG.md` + retroactive git tags + GitHub Releases (~1 day, highest ROI of the portfolio-narrative tasks).
       **Why this exists:** the GitHub repo entry currently shows a flat list of 70+ PRs with no architectural-milestone visual. Recruiters / interviewers don't piece architecture evolution together from PR titles. A CHANGELOG + Releases page IS the architecture timeline, in the place GitHub surfaces it most prominently.
       **Format:** [Keep a Changelog](https://keepachangelog.com/) at repo root. Each entry one-line summary + grouped under a milestone version.
       **Retroactive tags** at architectural milestones (don't tag every PR — tag the inflection points):
       - `v0.1.0` — Phase 1 baseline (synchronous Stage 1 + 2 architecture)
       - `v0.2.0` — Phase 1 complete + Redis Lua hot path + outbox + saga (Stage 4 architecture, pre-Phase-2)
       - `v0.3.0` — Phase 2 complete (CP1-CP9: reconciler + watchdog + checkpoints + alertmanager)
       - `v0.4.0` — Cache-truth roadmap complete (PR #73-#77: Makefile reset / rehydrate / NOGROUP alert / drift detector / outbox backlog)
       - `v0.5.0` — Pattern A core (D1-D6: reservation + payment + webhook + expiry sweeper)
       - `v0.6.0` — D7 saga scope narrowed + D8-minimal browser demo ← we are HERE as of 2026-05-08
       - `v1.0.0` — Phase 3 complete (D1-D16: Pattern A + comparison harness + minimal frontend + CHANGELOG + blog)
       **GitHub Releases page** for each tag — 2-3 paragraph release notes (can paste from CHANGELOG entry), list of included PRs (`Includes: #45, #46, ...`). The Releases URL becomes the resume bullet's anchor: "https://github.com/Leon180/booking_monitor/releases" replaces "browse my PR history" as the architectural-evolution surface.
       **Order:** D16 lands EARLY in Phase 3 (week 1-2) so the existing v0.4.0 milestone is captured BEFORE Pattern A starts adding new commits — retroactive tagging is much cleaner against a static SHA range than a moving HEAD.

### Post-PR-#90 (ticket_type cache) follow-ups — tracked, not yet PR'd

PR #90 landed the read-through Redis cache for `TicketTypeRepository.GetByID`, recovering ~57% of D4.1's sold-out fast-path RPS regression. Three rounds of multi-agent review (go-reviewer + silent-failure-hunter, then Staff-SRE + Staff-BE) produced a set of findings that were intentionally NOT in scope for the cache PR itself but should be addressed before D8 (multi-ticket-type-per-event expansion) lands. Listed in priority order:

| ID | Source | Scope | Trigger |
| :-- | :-- | :-- | :-- |
| **TT-Cache-1** | Staff-BE H1 | Replace `domain.TicketType` with a dedicated `TicketTypeSnapshot` (or equivalent) read-only DTO returned by the cache decorator. Today the decorator returns a `domain.TicketType` aggregate with `availableTickets=0` as a "conservative-safe sentinel". Works because no current caller branches on `AvailableTickets()` from a cache hit, but D8's ticket-type detail endpoint will. **Must fix before D8.** | D8 design phase |
| **TT-Cache-2** | Staff-BE H2 | Migrate cache wire-format versioning from `_v: 1` field to key-namespace (`ticket_type:v1:{id}`). Current `_v` mismatch-treated-as-miss pattern works for single-pod rolling deploy; fails for blue/green (both fleets amplify each other's miss load during the cutover window). | First blue/green or multi-pod rolling-deploy attempt |
| **TT-Cache-3** | Staff-BE M1 / Staff-SRE H3 | Migrate `idempotency_cache_get_errors_total` (legacy bare counter) to the labelled `cache_errors_total{cache,op}` shape. Heterogeneity complicates dashboards + alerts that want "alert on cache error rate across all caches". Cost: emit both for one TTL cycle (24h), then drop the legacy series. | Next observability cleanup pass |
| **TT-Cache-4** | Staff-BE M3 | OTEL span on cache hit (`ticket_type_cache.GetByID` with `cache.hit=true/false` attribute). Currently a hit emits no span; trace-driven latency debugging shows a gap at the GetByID step. Trivial to add (1-line `tracer.Start` wrap around the GET branch). | Before D8 (where ticket-type detail latency debugging matters) |
| **TT-Cache-5** | Staff-SRE M3 | Multi-ticket-type cold-fill bench. Current PR #90 benchmark drives 500K bookings against ONE ticket_type — pathological best-case hit rate. D8 expansion adds N ticket_types per event; verify the cache's behaviour at tier-opening time (cold-fill burst per tier). | D8 verification |
| **TT-Cache-6** | Staff-BE M4 | `singleflight.Group` keyed on `id.String()` to collapse cold-start thundering-herd PG calls into one. Not required for correctness today (concurrent fills are idempotent), but becomes load-bearing in D12's multi-binary comparison harness where 4 instances would multiply the PG load on cold-fill bursts. | D12 multi-binary harness |
| **TT-Cache-7** | Staff-SRE M4 | Document the rolling-upgrade hit-rate-collapse window in `d4.1_rollout.md` (or its successor). When `_v` is bumped or key-namespace versioning lands (TT-Cache-2), there's a transient cluster-wide hit-rate dip during the deploy. Currently undocumented as a known operational expectation. | Before TT-Cache-2 lands |

### Explicitly DEFERRED / DROPPED from Phase 3

| ID | Original scope | Why dropped |
| :-- | :-- | :-- |
| D11 | Live mission-control SSE dashboard (React Flow pipeline animation) | High build cost (~1-2 wk), low interview signal. Senior reviewers don't watch demos; they read code + benchmarks. The visual story is told via D14 mermaid diagrams + D15 blog series + D16 CHANGELOG/Releases + D12 comparison.md instead. |
| D13 | Frontend comparison view (Recharts side-by-side) | Same rationale. The markdown comparison report from D12 is interview-suitable on its own; an HTML chart adds polish but no signal. |
| D9 (original full scope) | Multi-stage k6 comparison + per-stage baselines | Folded into D12 — comparison.md captures all per-stage runs at once. Standalone D9-minimal keeps just the Pattern A two-step baseline. |
| D10 (original full scope) | 5-minute video walkthrough with mission-control overlay | Replaced by D10-minimal asciinema. Senior interview viewers prefer asciinema (proves CLI literacy + reproducibility) over edited video (proves nothing about engineering). |

Phase 4 (production-hardening, ~2–3 wk) — only after Pattern A demo lands
  P1  Redis HA (A9) — Sentinel + FailoverClient
  P2  Cross-process circuit breakers (A10*) — Redis + DB + Kafka + payment
  P3  k8s manifest / Kustomize base (N7) + HPA + PDB + probes
  P4  Auth / RBAC + gosec baseline (N9)
  P5  Backup / DR runbook + retention policy (N10)

Phase 5 (capstone benchmark, ~2 wk) — measure real bottleneck
  B1  k8s horizontal-scale benchmark (locks in the architecture story for portfolio)
  B2  config-tunables targeted optimization based on B1 findings
  B3  inventory sharding (CONDITIONAL on B1 showing single-key Redis CPU saturation)
```

## Cleanup PR (CP1) scope

Rows 1–9 from the checkpoint action plan, grouped logically:

| Theme | Items | Files touched |
| :-- | :-- | :-- |
| **Correctness fix** | Reconciler max-age must emit `order.failed` outbox event before MarkFailed (DEF-CRIT) | `internal/application/recon/reconciler.go`, possibly new factory in `internal/application/order_events.go` |
| **Doc drift (already in this PR)** | D1 5xx → 2xx; D2/D3/D4/D5/D6 currency | PROJECT_SPEC, CLAUDE.md, README.md (× 2 each), memory files |
| **Wire-format fix** | `NewOrderFailedEventFromOrder` factory; remove `Version: 0` bug in watchdog | `internal/application/saga/watchdog.go`, `internal/application/order_events.go` |
| **Ops** | scrape `payment_worker` + `recon` (verify each binary serves `/metrics`); 6 missing alerts (`idempotency_cache_get_errors_total`, `recon_mark_errors_total`, four `*_failures_total` counters); `HighLatency` window 1m → 5m | `deploy/prometheus/prometheus.yml`, `deploy/prometheus/alerts.yml` |
| **Security** | Cap pagination `size` at 100; `OrderStatus.IsValid()` + use in `StatusFilter` | `internal/infrastructure/api/booking/handler.go`, `internal/domain/order.go`, `internal/infrastructure/api/dto/request.go` |

CP1 is intentionally narrower than "everything-Critical" because rows 10–14 each warrant their own reviewable PR (architecture refactor, testcontainers, runbooks, Alertmanager).

## Demo plan — full narrative

The demo tells **two interleaved stories**: (1) architectural evolution under load (Stages 1→4 — why each layer exists), and (2) real-world e-commerce shape (Pattern A — reservation + webhook). Both are necessary because each answers a different interview question. The mission-control dashboard is the visual common ground that ties them together.

### User-visible flow (~5-minute demo script)

**Act 1 — "why each layer exists" (D11 + D12 + D13)**

1. **Open the live mission-control dashboard.** Pipeline diagram shows the full Stage 4 system idle, all queues empty.
2. **Switch the dashboard's `version_tag` filter to Stage 1.** Visual changes: the diagram dims out Redis / streams / Kafka / payment-worker / saga, leaving only `API → Postgres`.
3. **Push button: "Apply 5K RPS load to Stage 1".** Watch the dashboard: RPS rises briefly, then p99 hockey-sticks, conversion rate craters as request timeouts pile up. Stage 1 is at its row-lock-contention ceiling.
4. **Same load against Stage 4.** Dashboard renders smoothly: ~50K RPS sustained, p99 stays in the millisecond range, queue depth stays low because the worker keeps draining.
5. **Side-by-side comparison view (D13).** Recharts overlay: Stage 1 vs Stage 4 RPS-over-time + p99-over-time, with the annotated collapse points. The comparison archive lives in `docs/benchmarks/comparisons/`.

**Act 2 — "what real e-commerce does" (Pattern A)**

6. **Switch back to Stage 4. Open the booking page.** Click "Reserve". Frontend posts to `/api/v1/book` → 202 with `client_secret` + `reserved_until` countdown. Mission-control dashboard shows the order entering `awaiting_payment` state; inventory remaining ticks down; reservation TTL countdown is rendered alongside.
7. **Stripe Elements payment form** (mounted with `client_secret`). Submit card. Stripe.js confirms client-side. Backend `/webhook/payment` receives `payment_intent.succeeded`, verifies signature, transitions `awaiting_payment → paid`. Dashboard updates in real-time.
8. **Expiry sub-demo.** Reserve, then DON'T pay. Wait the TTL out. Reservation sweeper sweeps; dashboard shows `awaiting_payment_orders` decrementing, `expired_orders` incrementing, inventory ticking back UP because `revert.lua` ran. The same ticket can be re-reserved by another user.
9. **Saga compensation example.** Trigger a failure-mode (mock gateway 5xx). Webhook receives `payment_intent.payment_failed`. Saga compensator runs. Dashboard's saga lane lights up.

### Why this combined demo

- Stages 1–4 with mission-control video answers **"why this architecture, not just one Postgres?"** — most candidates can list patterns, few can show them collapse vs scale on the same dashboard.
- Pattern A answers **"why is this real-world e-commerce, not a CRUD toy?"** — Stripe / Ticketmaster / KKTIX architecture parity. Plus it solves the deeper business-vs-service-failure semantic gap noted in `architectural_backlog.md §13`.
- The interview talking point becomes "I built each layer separately, then composed them into the production-realistic shape, and you can watch each version's failure mode on the dashboard." Both depth and product sense.

### Out-of-repo decisions

- **Frontend** (`web/` workspace, Next.js): contains both the customer flow (Stripe Elements payment form) AND the mission-control dashboard + comparison view. Keep in repo so the demo is reproducible — clone repo, run `make demo-up`, recording-ready.
- **Stripe vs MockGateway**: ship with `MockStripeAdapter` (signed-webhook simulator) so the demo runs without Stripe credentials. Document the adapter swap in `docs/PROJECT_SPEC.md` §6.x. Real-Stripe operator instructions in `docs/runbooks/stripe-integration.md` (CP5).
- **Admin endpoint gating**: `ENABLE_ADMIN_DEMO=true` env var gates `/api/v1/admin/stream` and `/api/v1/admin/benchmark`. Off in any non-demo environment. Separate from `ENABLE_PPROF` so each surface can be turned on independently.

## O3.2 — Bare-metal benchmark (find the real ceiling)

**Why.** PR #71 ([docs/saturation-profile/20260502_221629_c500/](saturation-profile/20260502_221629_c500/)) established that under `make profile-saturation VUS=500 DURATION=60s` the system caps at **8,332 acc/s** with:

- Redis main thread CPU at **53%** (47% headroom)
- Pool: 0 misses, 0 timeouts, 23/200 conns in use (not pool-starved)
- Postgres: 2 in-use of pool, no waits (not DB-bound)
- ~33% Go CPU in `Syscall6` (writes 4× reads; ~11% HTTP response path, ~21% go-redis subtree)

The post-merge audit (canonical profile README's "Audit follow-up" section) confirmed both `Idempotency` and `BodySize` middleware are already correctly scoped — no cheap in-process fix to extract. So the remaining hypothesis the profile cannot rule out is: **a meaningful slice of the 33% Syscall6 cost is the docker-compose userspace network stack itself** (veth pair + iptables NAT + bridge traversal between k6, app, redis, postgres containers). To know how much of the 8,332 ceiling is "the architecture" vs "the test environment", re-measure under a different network shape.

### Experiment design

| Variant | Network shape | What it isolates | Effort |
| :-- | :-- | :-- | :-- |
| **A** (current baseline) | k6-in-docker → docker bridge → app-in-docker | Status quo (8,332 acc/s) | Already captured (PR #71) |
| **B** | host networking — `network_mode: host` for app + redis + postgres + k6 on a single Linux host | Removes veth + iptables overhead while keeping everything else the same | S–M (~1 day: compose override file + re-run + comparison report) |
| **C** | k6 on one cloud VM → app on another (managed Redis + managed PG, e.g. AWS) | Realistic production wire shape; reveals what someone deploying this would actually hit | M–L (~2–3 days: provision two VMs, terraform-light, capture profile + benchmark) |

For each variant, run the same `make profile-saturation` and compare:
- `accepted_bookings/sec` (the headline)
- `redis_cpu_total_rate` (does Redis become the bottleneck once network overhead is removed?)
- `cpu.pprof` `Syscall6` share (does the I/O cost shrink?)
- `bookings_total` p99 latency

Save the comparison to `docs/saturation-profile/<ts>_compare_<A|B|C>/comparison.md` mirroring the convention in `docs/benchmarks/`.

### Decision gates

| Outcome | Interpretation | Next move |
| :-- | :-- | :-- |
| B ≈ A (e.g. within 5–10%) | Docker network is not the cap | Revisit go-redis worker-side pipelining and HTTP/2 as the remaining levers — those are unmeasured estimates that variant B would still leave unanswered |
| B >> A (e.g. > 25%) | Docker network *is* a meaningful slice of the cap | Apply a "this measurement is local-docker-bound" caveat retroactively to the saturation profile findings; project narrative honestly notes "production ceiling is materially higher" |
| C reveals genuine architectural cap | That number is the resume bullet | Use C's number as the "what does this architecture actually do?" answer in interview / portfolio context |

### Out of scope

- Multi-instance horizontal benchmarks (multi-replica app + Redis Cluster). Those are downstream of B3 inventory sharding which was rejected for this profile config — re-evaluate only if variant C reveals a real architectural ceiling and there's a clear next-instance-of-the-same-architecture-doesn't-cut-it story.
- Cross-cloud / multi-region. Single-VM-pair (variant C) is enough to falsify "the docker overhead is most of what we're measuring".

### When to do it

This is the next experimentally-meaningful work after the demo arc. Order options:

1. **Pre-demo** — if the project narrative needs the bare-metal ceiling number for a resume / portfolio bullet ("X acc/s on a single-instance bare-metal stack")
2. **Post-demo** — if the demo's existing 8,332 acc/s headline is enough story by itself, and O3.2 is for personal-curiosity / interview-talking-point ("how would you find the actual ceiling?")

Recommendation: pre-demo for variant B (cheap), post-demo for variant C (more setup cost).

## Deferred / explicitly NOT in this roadmap

- **DLQ Worker** (formerly listed as next phase) — not blocking demo; defer until production traffic exists. Current DLQ + watchdog cover the recovery surface.
- **Event Sourcing / CQRS** — over-engineered for portfolio scope. `order_status_history` + outbox give us most of the audit value at fraction of the complexity.
- **Real payment gateway integration** beyond MockStripeAdapter — deferred until there is genuine business need. Adapter pattern keeps the door open.
- **Bulkhead pattern** + **feature flags** — see [memory: post_roadmap_pr_plan §6](../.claude/projects/-Users-lileon-project-booking-monitor/memory/post_roadmap_pr_plan.md). No business signal yet to justify either.

## References

- [Phase 2 checkpoint review](checkpoints/20260430-phase2-review.md) — canonical findings + action plan
- [Architectural backlog §13](#) — payment-failed semantic refactor + Pattern A roadmap (in agent memory)
- [Scaling roadmap](scaling_roadmap.md) — historical Stage 1–4 architecture narrative
- [`PROJECT_SPEC.md` §6.7](PROJECT_SPEC.md) — Charging two-phase intent log + reconciler design
- [`PROJECT_SPEC.md` §6.7.1](PROJECT_SPEC.md) — Saga watchdog design (asymmetry note)
