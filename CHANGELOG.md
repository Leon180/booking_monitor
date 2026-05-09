# Changelog

All notable architectural milestones in this project, written in reverse chronological order. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This is a portfolio / learning project, not a published library — versions mark **architecture inflection points**, not API stability promises. Use the GitHub Releases page (https://github.com/Leon180/booking_monitor/releases) for the rendered timeline; this file is the authoritative source.

## [Unreleased]

Post-v1.0.0 follow-ups not blocking the Phase 3 closure:

- **SLOWLOG + big-key observability** (scheduled cloud routine, due ~2026-05-22): `RedisSlowlogElevated` warning alert + `make redis-bigkeys` Make target.
- **TT-Cache-1 / TT-Cache-4 / TT-Cache-5 / TT-Cache-6** ([roadmap](docs/post_phase2_roadmap.md)) — only relevant if D8 demo expands to multi-ticket-type-per-event; the cache PR works correctly today for the single-ticket-type case.
- **D4.3** (deferred from D4.2 PR runbook) — dual-secret webhook rotation (`STRIPE_WEBHOOK_SECRET` + `STRIPE_WEBHOOK_SECRET_NEXT`) for zero-downtime rotation. Today's runbook acknowledges the brief mismatch-spike window during single-secret rotation.
- **O3.2 bare-metal benchmark** (parked) — find the real ceiling on dedicated hardware. Multi-day effort; runs only if the project pivots toward a true scale-test goal.

## [1.0.0] — 2026-05-09 — Phase 3 complete (D4.2 + D9/D10-minimal + D12 + D14 + D15)

Closes the Phase 3 portfolio arc started 2026-04-30. Pattern A is end-to-end against a real payment provider, the architectural-evolution comparison harness ships across four binaries, and the portfolio narrative (mermaid diagrams + 4 blog posts + asciinema walkthrough) lands in repo for senior-interview consumption.

The roadmap's v1.0.0 anchor was "D1-D16 complete" — D1-D6 shipped in v0.5.0, D7+D8-minimal+D16 shipped in v0.6.0, this release brings D4.2 + D9-minimal + D10-minimal + D12 + D14 + D15 together.

### Added — D4.2 (real Stripe SDK adapter)

- **D4.2 ([#110](https://github.com/Leon180/booking_monitor/pull/110); 2026-05-09)** — Replaced the in-process mock payment gateway with a real `stripe-go v82.5.1` SDK adapter at [`internal/infrastructure/payment/stripe_gateway.go`](internal/infrastructure/payment/stripe_gateway.go). Selection is config-driven via `PAYMENT_PROVIDER ∈ {mock, stripe}`; mock stays in-tree for `make stress-k6` + unit tests; production uses Stripe.
  - `stripe.NewClient(key, WithBackends(...))` builder pattern (no global SDK state mutation; multi-adapter-safe under `-race`). `BackendConfig` injects the project HTTP client + redacting `LeveledLogger` (strips bearer tokens before any log line lands) + `MaxNetworkRetries`.
  - 4 new `domain.ErrPayment*` sentinels (Declined / Transient / Misconfigured / Invalid) translated from `*stripe.Error` types via `mapStripeError` using `errors.Join` (preserves `errors.Is` on both sides). Stripe `Request-Id` captured + threaded into log fields for cross-system correlation.
  - `PaymentStatusReader.GetStatus` interface change: `(ctx, orderID uuid.UUID)` → `(ctx, paymentIntentID string)`. Layer-clean — Stripe API has no orderID concept; reconciler reads persisted `payment_intent_id` from order row, threads it through, and null-guards `strings.TrimSpace == ""` (recon emits `recon_null_intent_id_skipped_total` for the orphan case).
  - Webhook verifier delegated to `stripewebhook.ValidatePayloadWithTolerance`; typed sentinels preserved via `mapStripeWebhookError` so `payment_webhook_signature_invalid_total{reason}` taxonomy survives SDK upgrades. New `ErrConfigError` sentinel.
  - 2 new metrics: `stripe_api_calls_total{op,outcome}` + `stripe_api_duration_seconds{op}` (`ExponentialBuckets(0.01, 2, 14)` covering 10ms→81.92s). Pre-warmed labels.
  - Production-mode validation rejects `mock` provider, `*_test_*` keys, `pk_live_*` (publishable key), AND any non-production env where Stripe credentials are paired with `PAYMENT_PROVIDER=mock` (round-4 staging-mock-silent-money-void guard).
  - Compose env passthrough + `.env.example` entries for `PAYMENT_PROVIDER`, `STRIPE_API_KEY`, `STRIPE_WEBHOOK_SECRET`, `STRIPE_API_TIMEOUT`, `STRIPE_MAX_NETWORK_RETRIES`, `STRIPE_ALLOW_TEST_KEY` with safe defaults.
  - Bilingual docs (5 paired files) + runbook section: PCI scope statement (out of SAQ A scope — Stripe Elements client-side), 6-step sandbox→live cutover checklist, webhook secret rotation playbook, leaked-key incident response.
  - **4 rounds of multi-agent review** (per-slice + pre-merge + post-open + post-smoke) with full action ledgers in PR body. Live smoke against Stripe test mode validated end-to-end: real PaymentIntent minted, idempotency verified, signed webhook accepted, metrics emitted.

### Added — D12 (4-version comparison harness)

- **D12 ([#104](https://github.com/Leon180/booking_monitor/pull/104) → [#109](https://github.com/Leon180/booking_monitor/pull/109); 2026-05-08/09)** — Senior-interview architectural-evolution talking point. Four `cmd/booking-cli-stage{1..4}/` binaries against the SAME `internal/` packages (different fx wirings) prove the value of each architectural layer empirically.
  - **Stage 1** ([#104](https://github.com/Leon180/booking_monitor/pull/104)) — API → Postgres `SELECT FOR UPDATE`. No Redis, no Kafka, no async, no saga. Pure synchronous baseline; ~30 LOC main.go.
  - **Stage 1 refactor** ([#105](https://github.com/Leon180/booking_monitor/pull/105)) — Extracted Stage 1 HTTP handlers to `internal/infrastructure/api/stagehttp/` so Stages 2/3/4 can reuse without copy-paste.
  - **Stage 2** ([#106](https://github.com/Leon180/booking_monitor/pull/106)) — API → Redis Lua atomic deduct → SYNCHRONOUS DB write. Inventory in Redis but no async buffering.
  - **Stage 3** ([#107](https://github.com/Leon180/booking_monitor/pull/107)) — API → Redis Lua → `orders:stream` → worker → DB. Async + worker pool, no event-driven downstream.
  - **Stage 4** ([#108](https://github.com/Leon180/booking_monitor/pull/108)) — Stage 3 + Kafka outbox + saga compensator. Plus saga compensator end-to-end loop duration histogram + consumer lag-since-write gauge + 13-outcome `saga_compensator_events_processed_total` taxonomy.
  - **PR-D12.5** ([#109](https://github.com/Leon180/booking_monitor/pull/109)) — Multi-target k6 harness + auto-generated `comparison.md` per run at `docs/benchmarks/comparisons/<TS>_4stage_c500_d60s/comparison.md`. Pure-intake k6 scenario (Ticketmaster / Stripe / Shopify funnel-stage decomposition pattern) reports BOTH `http_reqs/s` (capacity at load-shed gate) and `accepted_bookings/s` (booking hot path). Stage 1 row-lock plateau ~1,643/s; Stages 2-4 cluster ~8,400/s network/CPU bound.

### Added — D9-minimal / D10-minimal (demo polish)

- **D9-minimal + D10-minimal ([#102](https://github.com/Leon180/booking_monitor/pull/102), [#103](https://github.com/Leon180/booking_monitor/pull/103); 2026-05-08)** — k6 scenario script for the two-step reservation+payment flow at `scripts/k6/two_step.js`; baseline capture under [`docs/benchmarks/20260509_014318_two_step_baseline_c100_d90s/`](docs/benchmarks/20260509_014318_two_step_baseline_c100_d90s/). Asciinema terminal walkthrough at [`docs/demo/walkthrough.cast`](docs/demo/walkthrough.cast) (~2-3 minutes: `make demo-up` → curl reservation → curl payment → curl `/api/v1/orders/:id` showing state transitions). Embedded link in README.

### Added — D14 / D15 (portfolio narrative)

- **D14 README mermaid refresh ([#100](https://github.com/Leon180/booking_monitor/pull/100); 2026-05-08)** — Pattern A architecture diagram + cross-link from PROJECT_SPEC. 6 mermaid blocks total in README rendering the booking flow / saga loop / outbox path / Redis hot path.
- **D15 engineering blog series — 4 posts × bilingual (EN + zh-TW)**:
  - [`2026-05-cache-truth-architecture.md`](docs/blog/2026-05-cache-truth-architecture.md) — the FLUSHALL incident → 411-of-1000 silent message loss → cache-truth contract evolution.
  - [`2026-05-detect-but-dont-fix.md`](docs/blog/2026-05-detect-but-dont-fix.md) — drift detection without auto-correction; the operational discipline trade-off.
  - [`2026-05-lua-single-thread-ceiling.md`](docs/blog/2026-05-lua-single-thread-ceiling.md) — single-key Lua serialization as the physics ceiling at 8,330 acc/s, found via VU scaling.
  - **[`2026-05-saga-pure-forward-recovery.md`](docs/blog/2026-05-saga-pure-forward-recovery.md) ([#101](https://github.com/Leon180/booking_monitor/pull/101); 2026-05-09)** — saga shouldn't manage the happy path; D7 narrowing as Garcia-Molina 1987 §5's engineering implementation.

### Hardened

- All 4 D4.2 multi-agent review rounds were actioned: 1 HIGH (compile-time interface assertions for StripeGateway), 7 MEDIUMs (redactJSONField unterminated-string fail-safe, whitespace `PaymentIntentID` null-guard via TrimSpace, `GetStatus` error-path metric tests, dead `captureLogger` test helper deletion, staging mock-mode silent money-void cross-field guard, etc.), and 7 LOWs (`pk_live_` rejection, `mapStripeStatusToCharge` ctx threading, runbook step ordering, `.env.example` entries, etc.). Two findings were verified-not-real and skipped with rationale documented in the PR.

### Compare

- v0.6.0…v1.0.0: https://github.com/Leon180/booking_monitor/compare/v0.6.0...v1.0.0

## [0.6.0] — 2026-05-08 — D7 saga scope narrowed + D8-minimal browser demo

D8-minimal opens the customer-facing browser demo surface for the first time — Pattern A's `book → pay → terminal` flow now runs in a real browser tab against a CORS allow-list. D7 simultaneously narrows saga scope by deleting the legacy A4 auto-charge path that v0.5.0 left behind for backwards-compat — saga compensator's `order.failed` topic now has only two production emitters (D5 webhook + D6 expiry sweeper).

### Added

- **D7 saga scope narrowed ([#98](https://github.com/Leon180/booking_monitor/pull/98); 2026-05-08)** — Deleted the legacy A4 auto-charge path entirely: `payment.Service.ProcessOrder`, `messaging/kafka_consumer.go` (the `order.created` consumer), `cmd/booking-cli/payment.go` subcommand, `payment_worker` docker-compose service, `payment-worker` Prometheus scrape job, `domain.PaymentCharger` interface + `PaymentGateway.Charge` method + `MockGateway.Charge`, `domain.EventTypeOrderCreated` + `NewOrderCreatedOutbox`, `application.OrderCreatedEvent` + `NewOrderCreatedEvent` + `NewOrderFailedEvent(from OrderCreatedEvent)`, `KafkaConfig.PaymentGroupID` + `OrderCreatedTopic`, `MockGateway.SuccessRate` field + `results` sync.Map + `Charge` idempotency tests, `ErrInvalidPaymentEvent` sentinel. `payment.Service` interface narrowed to `CreatePaymentIntent` only; `payment.NewService` parameter narrowed from `domain.PaymentGateway` to `domain.PaymentIntentCreator` (fx provider advertises that narrow type via `fx.As`). `MockGateway.GetStatus` re-narrated to always return `ChargeStatusNotFound` post-D7 (no charge history to look up). `order.failed` saga events now have only two production emitters: D5 webhook (`payment_failed`) and D6 expiry sweeper (`expired`); `recon.failOrder` is a third (rare) emitter for stuck-charging force-fails, explicitly tagged via `Reason="recon: ..."`. Saga consumer was always in-process inside `app`. `kafka_consumer_retry_total` label set narrowed from `{topic=order.created|order.failed}` to `{topic=order.failed}` only. **Worker UoW shape change**: `[INSERT order, INSERT events_outbox(order.created)]` → `[INSERT order]`; hot-path benchmark report at [`docs/benchmarks/20260508_compare_c500_d7/`](docs/benchmarks/20260508_compare_c500_d7/) shows non-regressive (booking p95 -8.7%, http p95 -9.7%, `accepted_bookings/s` flat). Bilingual docs sweep (README, PROJECT_SPEC, AGENTS, CLAUDE, monitoring) + agent-rule docs (`patterns.md`, `coding-style.md`) + runbooks (incl. D7 cutover note for draining pending `order.created` outbox rows pre-deploy).
- **D8-minimal browser demo (PRs [#96](https://github.com/Leon180/booking_monitor/pull/96), [#97](https://github.com/Leon180/booking_monitor/pull/97); 2026-05-07/08)** — PR-1: opt-in CORS middleware (`internal/infrastructure/api/middleware/cors.go`) with exact-match Origin allow-list + `Vary: Origin` on every response + ACRM/ACRH scoped to OPTIONS; new `AppConfig.Env` field + `normalizedAppEnv()` helper enforces "empty/whitespace → production" fail-closed, with a production-mode guard that rejects `ENABLE_TEST_ENDPOINTS=true`. PR-2: single-page Vite + React + TS demo at `demo/` exercising the full Pattern A flow (book → pay/let-expire → terminal) with intent-aware status display (`(intent, observed_status) → display`) so the saga's `payment_failed → compensated` transition between two 1 Hz polls is rendered correctly. Mock-only — confirm step uses `POST /test/payment/confirm/:order_id` (forges signed webhook). Live smoke 2026-05-08 verified CORS contract + Path A (paid) + Path B (declined → compensated) + Path C (expired) + demo bundle inlining.

### Compare
- v0.5.0…v0.6.0: https://github.com/Leon180/booking_monitor/compare/v0.5.0...v0.6.0

## [0.5.0] — 2026-05-07 — Pattern A end-to-end (D1–D6)

Closes the v0.5.0 demo arc — the customer-facing booking flow now matches Stripe Checkout / KKTIX shape. The legacy `POST /book` auto-charge path is split into three stages: reservation hold (D1–D3), Stripe-shape `PaymentIntent` creation (D4 / D4.1), and provider webhook resolution (D5). The previously-missing piece — what happens when the customer abandons checkout — closes via D6's reservation expiry sweeper.

End-to-end runtime against the Docker stack: `book → /pay → confirm OR expire → paid OR compensated`.

### Added
- **D1** ([#84](https://github.com/Leon180/booking_monitor/pull/84)) — Schema migration 000012: `event_sections`, `orders.section_id`, `orders.reserved_until`, `orders.payment_intent_id`, `events.reservation_window_seconds`, partial index `idx_orders_awaiting_payment_reserved_until` (consumed by D6).
- **D2** ([#85](https://github.com/Leon180/booking_monitor/pull/85)) — Domain state machine: additive Pattern A transitions `Pending → AwaitingPayment → Paid | Expired | PaymentFailed → Compensated`. Legacy A4 edges retained until D7.
- **D3** ([#86](https://github.com/Leon180/booking_monitor/pull/86)) — `POST /api/v1/book` returns 202 with `status:"reserved"` + `reserved_until` + `links.pay`. Worker persists row as `awaiting_payment` (no auto-charge).
- **D4** ([#87](https://github.com/Leon180/booking_monitor/pull/87)) — `POST /api/v1/orders/:id/pay` creates Stripe-shape `PaymentIntent`. Idempotent on `order_id` (gateway-side `Idempotency-Key` convention).
- **D4.1** ([#89](https://github.com/Leon180/booking_monitor/pull/89)) — KKTIX 票種 model: `event_sections` → `event_ticket_types`, price snapshot frozen at book time on `orders.amount_cents` / `currency`, `BOOKING_DEFAULT_TICKET_PRICE_CENTS` global removed.
- **PR-Cache** ([#90](https://github.com/Leon180/booking_monitor/pull/90), superseded) — Read-through cache for `TicketTypeRepository.GetByID` recovered the −40% sold-out RPS regression D4.1 introduced.
- **PR-Lua-Meta** ([#91](https://github.com/Leon180/booking_monitor/pull/91)) — Moved ticket-type immutable metadata into Redis (`ticket_type_meta:{id}` HASH consumed directly by `deduct.lua`). Single Redis round-trip per booking; +13% RPS / -28% p95 vs the cache decorator. Cache decorator deleted.
- **D5** ([#92](https://github.com/Leon180/booking_monitor/pull/92)) — `POST /webhook/payment`: HMAC-SHA256 signature verifier (Stripe convention), order resolution via `metadata.order_id` primary + `payment_intent_id` fallback, intent-mismatch guard, race-aware `MarkPaid` SQL (`reserved_until > NOW()` predicate), late-success refund path with `detected_at` label split (service_check / sql_predicate / post_terminal). 7 new metrics, 4 new alerts. Mock confirm test endpoint gated by `ENABLE_TEST_ENDPOINTS`. Migration 000015 partial unique index on `orders.payment_intent_id`.
- **D6** ([#94](https://github.com/Leon180/booking_monitor/pull/94)) — `booking-cli expiry-sweeper` subcommand. DB-NOW SQL form `WHERE reserved_until <= NOW() - $1::interval` shares time source with D5's MarkPaid predicate. **MaxAge does NOT gate the transition** (saga's "Redis state unknown" rationale doesn't apply to D6, which never touches Redis directly); MaxAge is observability-only via `expired_overaged` outcome label + dedicated `expiry_max_age_total` counter (counter fires only on commit). Per-row UoW `MarkExpired + Outbox.Create` mirrors D5's `handleLateSuccess`. 7 new metrics + 4 new alerts. Round-3 F2 contract: post-sweep count failure holds gauges at last-known-good.

### Demo arc
The end-to-end flow can be exercised against the Docker stack via `scripts/d6_smoke.sh` (book → never confirm → poll until compensated → verify `event_ticket_types.available_tickets` baseline restored). Smoke uses `BOOKING_RESERVATION_WINDOW=20s` override for ~62s total cycle time.

### Tagged off `main`
Code boundary: `a0da8a8` (D6 merge — last code commit included in v0.5.0). Release-note commit: `aa1c93e` (PR [#95](https://github.com/Leon180/booking_monitor/pull/95) prepared this CHANGELOG section + README release wording). Tag `v0.5.0` points at `aa1c93e` so `git checkout v0.5.0` includes the section that documents itself. 4 plan-rounds + 4 implementation-review-rounds of Codex feedback baked in across D5 + D6.

## [0.4.0] — 2026-05-03 — Cache-truth architecture

Closes the silent-message-loss and inventory-drift detection gap surfaced when the FLUSHALL incident showed 411-of-1000 messages had been silently dropped. Establishes the contract: **Redis is ephemeral, Postgres is the source of truth, drift is detected and named.**

### Added
- **PR-A** ([#73](https://github.com/Leon180/booking_monitor/pull/73)) — `make reset-db` switched from FLUSHALL to precise DEL on cache keys; preserves Redis stream + consumer group across resets so test reset stops being a NOGROUP-trigger.
- **PR-B** ([#74](https://github.com/Leon180/booking_monitor/pull/74)) — App-startup `RehydrateInventory` (SETNX-not-SET, advisory-lock-serialised across multi-instance startup); explicit `appendonly no` + `save ""` Redis config marking ephemeral by design.
- **PR-C** ([#75](https://github.com/Leon180/booking_monitor/pull/75)) — `consumer_group_recreated_total` counter + `ConsumerGroupRecreated` critical alert on NOGROUP self-heal events; pairs with runbook so the silent-message-loss case is detectable instead of buried.
- **PR-D** ([#76](https://github.com/Leon180/booking_monitor/pull/76)) — `InventoryDriftDetector` co-resident with the reconciler in `recon` subcommand. Compares Redis cached qty vs Postgres `events.available_tickets` every 60s; three direction labels (`cache_missing` / `cache_high` / `cache_low_excess`) route to distinct runbook branches.
- **Outbox backlog gap** ([#77](https://github.com/Leon180/booking_monitor/pull/77)) — `outbox_pending_count` gauge from a per-scrape COUNT against the `events_outbox` partial index; emits 0 on DB failure (not stale) so `OutboxPendingBacklog` warning + `OutboxPendingCollectorDown` critical alerts compose correctly.

### Hardened
- Sweep goroutine panic recovery in all three sweepers (Reconciler, InventoryDriftDetector, SagaWatchdog) via `safeSweep` helper with `runtime/debug.Stack()` capture + `sweep_goroutine_panics_total{sweeper}` counter + `SweepGoroutinePanic` critical alert. Closes the "loop dies silently while process stays up, /metrics keeps serving stale gauges" silent-failure shape.

### Documentation
- New runbook sections for every alert added in this milestone
- Bilingual `docs/monitoring.md` + `docs/monitoring.zh-TW.md` updates (§2 metric inventory, §5 alert catalog, §5 force-fire recipes)
- [`docs/architectural_backlog.md`](docs/architectural_backlog.md) § "Cache-truth architecture" — full sequence rationale + the 411/1000 incident analysis

### Compare
- v0.3.0…v0.4.0: https://github.com/Leon180/booking_monitor/compare/v0.3.0...v0.4.0

## [0.3.0] — 2026-05-02 — Phase 2 reliability sprint + observability O3 + O3.1

Senior-review checkpoint completed; reconciler + saga watchdog landed; integration test suite established; full Alertmanager + runbook surface; multi-process metrics scraping closed.

### Added
- **Phase 2 sprint (PRs [#45](https://github.com/Leon180/booking_monitor/pull/45)–[#49](https://github.com/Leon180/booking_monitor/pull/49))**:
  - **A4** Charging two-phase intent log + `recon` subcommand — closes the "worker crashed mid-Charge" silent-failure path
  - **N4** Stripe-style idempotency-key fingerprint validation (body-hash mismatch → 409)
  - `POST /book` response shape (returns `order_id` + `status` + self link); `GET /api/v1/orders/:id` poll endpoint
  - **A5** Saga watchdog + project-review checkpoint framework
- **Phase 2 cleanup sprint (PRs [#51](https://github.com/Leon180/booking_monitor/pull/51)–[#65](https://github.com/Leon180/booking_monitor/pull/65))**:
  - **CP1** action-list cleanup (recon outbox emit + 6 alerts + S1/S2 hardening)
  - **CP2 / CP2.5 / CP2.6a / CP2.6b** application-layer architecture cleanup (Config/Metrics interfaces extracted, port relocations, subpackage tidy)
  - **CP3a / CP3b** observability/metrics.go split + middleware relocation
  - **CP4a / CP4b / CP4c** testcontainers integration suite (Postgres harness + 40 tests across Order / Event / Outbox / UoW / migration round-trip)
  - **CP5 + CP6** consolidated runbooks + Alertmanager wiring (severity-specific cadences, inhibition rules)
  - **CP7** test-surface gaps closed on saga / event / outbox.Run / handlers
  - **CP8** N4 fingerprint cost calibration via header-bearing benchmark
  - **CP9** Grafana dashboard panels for recon / saga / DLQ / DB pool / cache
- **Observability sprint (PRs [#52](https://github.com/Leon180/booking_monitor/pull/52), [#66](https://github.com/Leon180/booking_monitor/pull/66)–[#72](https://github.com/Leon180/booking_monitor/pull/72))**:
  - **O3** worker `/metrics` listeners + Prometheus scrape jobs for `payment-worker` / `recon` / `saga-watchdog`
  - **O3.1a** Redis-server-side metrics via `oliver006/redis_exporter`
  - **O3.1b** Redis client-pool metrics + `make profile-saturation` saturation diagnostic tool
  - **O3.1c** middleware-scoping audit + O3.2 plan (bare-metal benchmark for finding the real ceiling)
  - VU scaling stress test ([#68](https://github.com/Leon180/booking_monitor/pull/68)) characterising the 8,330 acc/s booking hot-path saturation point — single-key Lua serialisation is the physics ceiling, not the Redis CPU.

### Documentation
- [`docs/checkpoints/20260430-phase2-review.md`](docs/checkpoints/20260430-phase2-review.md) — first project-review checkpoint, grade A−
- [`docs/post_phase2_roadmap.md`](docs/post_phase2_roadmap.md) — forward-looking roadmap superseding memory-only sequencing
- [`docs/runbooks/README.md`](docs/runbooks/README.md) — consolidated alert runbook (Stripe / Lyft style; one anchor per alert)
- [`docs/saturation-profile/`](docs/saturation-profile/) — canonical example of the saturation diagnostic output

### Compare
- v0.2.0…v0.3.0: https://github.com/Leon180/booking_monitor/compare/v0.2.0...v0.3.0

## [0.2.0] — 2026-04-XX — Stage 4 architecture (full async)

Full booking pipeline: API → Redis Lua atomic deduct → orders:stream → worker → Postgres + transactional outbox → Kafka → payment service + saga compensator. Hardened across 15 phases of refinement (initial implementation, security review, GC perf tuning, structured logging refactor, bilingual docs adoption).

### Added
- **Hot path**: Redis Lua atomic deduct (`DECRBY` + `XADD` in one script) gated by `event:{uuid}:qty` key; sold-out detection via Lua revert path
- **Async pipeline**: Redis Streams consumer group with PEL recovery; Kafka outbox pattern with advisory-lock-leadered relay
- **Reliability**: idempotency at 4 layers (`Idempotency-Key` HTTP header / DB UNIQUE constraint / Redis SETNX in saga / MockGateway sync.Map); saga compensation via `order.failed` Kafka topic; per-message retry budget with DLQ routing
- **Domain modeling**: immutable entity factories (`NewOrder` / `NewEvent` / `NewOutboxEvent`) with invariant validation; UUIDv7 caller-generated IDs threaded end-to-end (handler → queue → worker → DB → outbox → saga); explicit `OrderStatus` typed transitions
- **Observability**: 31+ Prometheus metrics (RED + USE + domain), OTEL distributed tracing with trace ↔ log correlation, structured logging with auto-injected `correlation_id`/`trace_id`/`span_id`
- **Operations**: graceful shutdown via fx lifecycle, k8s-style `/livez` + `/readyz` health probes, multi-stage Dockerfile (non-root, version-pinned base image)
- **Performance**: GC tuning (sync.Pool + GOMEMLIMIT + GOGC=400), 157% RPS recovery from a regression (PR [#14](https://github.com/Leon180/booking_monitor/pull/14)), combined HTTP middleware
- **Testing**: testify + go.uber.org/mock + race detector mandatory in CI

### Documentation
- Bilingual EN + zh-TW for [`AGENTS.md`](AGENTS.md), [`.claude/CLAUDE.md`](.claude/CLAUDE.md), [`README.md`](README.md), [`docs/PROJECT_SPEC.md`](docs/PROJECT_SPEC.md), [`docs/monitoring.md`](docs/monitoring.md). PostToolUse hook enforces structural parity at edit time.
- 16 alerts in [`deploy/prometheus/alerts.yml`](deploy/prometheus/alerts.yml) with comment-block rationale per alert

### Hardened (review-driven)
- All 6 CRITICAL findings resolved (PR [#8](https://github.com/Leon180/booking_monitor/pull/8))
- All 13 HIGH findings resolved (PR [#9](https://github.com/Leon180/booking_monitor/pull/9))
- 17 MEDIUM + 14 LOW + 6 NIT findings resolved (PR [#12](https://github.com/Leon180/booking_monitor/pull/12))

### Compare
- v0.1.0…v0.2.0: https://github.com/Leon180/booking_monitor/compare/v0.1.0...v0.2.0

## [0.1.0] — 2026-03-XX — Synchronous baseline + saturation benchmark

Initial Stage 1 architecture (API → Postgres `SELECT FOR UPDATE`, no Redis, no async, no Kafka, no saga). Baseline benchmark documents the row-lock contention ceiling that motivates everything in v0.2.0+.

### Added
- Synchronous booking flow: `POST /book` validates input, takes a row lock on the event, decrements `available_tickets`, inserts the order — all in one Postgres transaction
- k6 load testing harness ([`scripts/`](scripts/)) + scaling roadmap ([`docs/scaling_roadmap.md`](docs/scaling_roadmap.md))
- C500 (concurrency=500) benchmark establishing the synchronous-architecture ceiling — documented in early `docs/benchmarks/` entries

### Findings (drove the v0.2.0 redesign)
- Synchronous architecture saturates well below 1k req/s due to PG row-lock contention on the hot inventory row
- Each request blocks the whole transaction including the row lock; all concurrent bookers serialise behind one another
- The "obvious" fix — pessimistic row lock + retry — doesn't help because the lock IS the bottleneck

### Compare
- Initial commit…v0.1.0: https://github.com/Leon180/booking_monitor/compare/65502bb...v0.1.0
