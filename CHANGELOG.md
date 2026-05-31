# Changelog

All notable architectural milestones in this project, written in reverse chronological order. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This is a portfolio / learning project, not a published library — versions mark **architecture inflection points**, not API stability promises. Use the GitHub Releases page (https://github.com/Leon180/booking_monitor/releases) for the rendered timeline; this file is the authoritative source.

## [1.2.0](https://github.com/Leon180/booking_monitor/compare/v1.1.0...v1.2.0) (2026-05-31)


### Features

* **admin-stream:** per-subsystem resource monitoring + buffer-pool decision ([eca2ff8](https://github.com/Leon180/booking_monitor/commit/eca2ff8fd0b0e122cb77078e185af853518d12b3))
* **admin-stream:** per-subsystem resource monitoring + buffer-pool decision report ([996c3f9](https://github.com/Leon180/booking_monitor/commit/996c3f9ad12e5dbee3314ae697404dc460f684a7))
* **admin:** AdminEvent envelope + 8 typed payloads + factories ([529a792](https://github.com/Leon180/booking_monitor/commit/529a7920bd267e0aa83d480a244f0182158f8cef))
* **admin:** event bus with bounded-async drop policy ([63f0ee3](https://github.com/Leon180/booking_monitor/commit/63f0ee3c1aa32edd85c4a3e6c037e1edbf22ef94))
* **admin:** war-room dashboard HTML + interview talking-point sheet + gitignore fix ([0febfcf](https://github.com/Leon180/booking_monitor/commit/0febfcf41370e7523fa428e63aaa8caf2d4fce0c))
* **admin:** war-room dashboard HTML + interview talking-points ([44c6a63](https://github.com/Leon180/booking_monitor/commit/44c6a6366d043616d675a6382945f269106fe5d4))
* **bootstrap:** fx wiring + graceful drain lifecycle for admin SSE ([86d05ee](https://github.com/Leon180/booking_monitor/commit/86d05ee3b94dc26350915c6bce124eb94fe9a4ec))
* **cli,server:** admin-token mint + SSE route wiring on booking-cli ([349ec42](https://github.com/Leon180/booking_monitor/commit/349ec42b0c2cb551dd36c35a11bcbdbd20fddd71))
* **demo:** add demo-stage5-rush-edge — through-nginx flash-sale demo ([a303b7a](https://github.com/Leon180/booking_monitor/commit/a303b7ab8ad8edea64fe52784385f2ef6d8bb2fd))
* **demo:** grafana-open target + canonical-aligned demo-stage5-rush ([ec3b091](https://github.com/Leon180/booking_monitor/commit/ec3b09154d002e8b173624cfd1ef4b8064628e43))
* **deploy:** GitHub Actions deploy workflow — WIF + cosign + smoke (PR 5/8) ([#136](https://github.com/Leon180/booking_monitor/issues/136)) ([1aad65d](https://github.com/Leon180/booking_monitor/commit/1aad65d4b2390308c5090dd3ea2a455d05587343))
* **deploy:** operator deploy pipeline — Cloudflare Tunnel + migrate + secrets sync (PR 4/8) ([#135](https://github.com/Leon180/booking_monitor/issues/135)) ([48cca9c](https://github.com/Leon180/booking_monitor/commit/48cca9c01793881f53f8b3ee545dc672c080099b))
* **middleware:** JWT HS256 admin auth + MintAdminJWT helper ([8ef4f11](https://github.com/Leon180/booking_monitor/commit/8ef4f115668d12c7151504a30931e91ad1690512))
* **observability:** admin stream metrics + inventory_low_alerts_total ([c540537](https://github.com/Leon180/booking_monitor/commit/c5405371e47d4e94227255f5fe16fa13eb7e5d7d))
* **sse:** hub + subscriber + handler (subscribe-then-replay) ([111d4ab](https://github.com/Leon180/booking_monitor/commit/111d4abfd632cb75cf631f05d008b48cbad668fb))
* **stage5:** wire admin SSE event stream + war-room dashboard into demo binary ([b169bd4](https://github.com/Leon180/booking_monitor/commit/b169bd406b9190c574b686d9dd2ea2da02d64c6b))
* **stage5:** wire admin SSE event stream + war-room dashboard into demo binary ([6bf745b](https://github.com/Leon180/booking_monitor/commit/6bf745b33aa4b0fbe9c077309bc716a0e312bf55))
* **streaming:** SSE admin event stream — design + domain layer (WIP) ([d7b44e3](https://github.com/Leon180/booking_monitor/commit/d7b44e3933431d942a801e9f092fa1eaef829eee))


### Bug Fixes

* **deps:** bump otel + grpc to patch Trivy HIGH/CRIT CVEs ([#140](https://github.com/Leon180/booking_monitor/issues/140)) ([1a79907](https://github.com/Leon180/booking_monitor/commit/1a79907ff5c7142bc6eeaa204f846b8db0b3763b))
* **release:** attest job — docker login for oras-based AR push ([#141](https://github.com/Leon180/booking_monitor/issues/141)) ([dd8958b](https://github.com/Leon180/booking_monitor/commit/dd8958b847f3ec5a80ab73e7416d921ef1d4ae84))
* **release:** correct trivy-action ref to v0.36.0 (v-prefix required) ([#138](https://github.com/Leon180/booking_monitor/issues/138)) ([a06c7ac](https://github.com/Leon180/booking_monitor/commit/a06c7ac851d16e3f2dbc5bfe05fc6b0b4a70a5be))
* **sse:** eliminate two test-self-race conditions in hub_test ([a05fa0d](https://github.com/Leon180/booking_monitor/commit/a05fa0dc2d81281285bd29b82d2be6776f61f76e))
* **streaming:** address multi-agent review round 1 (CRIT-1/2 + 4 HIGH + 4 MED) ([06515fc](https://github.com/Leon180/booking_monitor/commit/06515fc2f4037d1383cdea5ab128b1bb6ead1104))
* **streaming:** address multi-agent review round 2 (2 REG + 1 NEW) ([95c510b](https://github.com/Leon180/booking_monitor/commit/95c510be9f60b65d011bbe8ace752f28bbbb7ebb))
* **streaming:** address multi-agent review round 3 (3 real + 1 false-positive verified) ([6648fee](https://github.com/Leon180/booking_monitor/commit/6648fee3fc33d9e0e6b81312ca1bc4afe324b087))
* **streaming:** smoke fixes — *redis.Client + worker publish wiring ([89f4869](https://github.com/Leon180/booking_monitor/commit/89f4869ee09da6c6d7e93dfddb1f0707d2a83ee7))
* **streaming:** smoke round 4 — wire stage3 integration test to NoopBus ([614bff1](https://github.com/Leon180/booking_monitor/commit/614bff1e74afc1c9b3acfac085e7608da8e2d807))


### Refactoring

* **stage5:** move cmd-resident business logic into internal/ ([#119](https://github.com/Leon180/booking_monitor/issues/119)) ([2dea898](https://github.com/Leon180/booking_monitor/commit/2dea8983013e485506082db8389410a9b399f586))

## [Unreleased]

Post-v1.1.0 follow-ups:

- **SLOWLOG + big-key observability** (scheduled cloud routine, due ~2026-05-22): `RedisSlowlogElevated` warning alert + `make redis-bigkeys` Make target.
- **Payment retry window** (known UX debt): `payment_failed` webhook currently fires saga compensation immediately; should preserve `awaiting_payment` state for retry within `reserved_until` window.
- **Webhook provider event-ID dedup** (hardening): store Stripe `evt_*` ID to make double-delivery provably idempotent at the HTTP boundary, not just at the state-machine layer.
- **WORKER_ID uniqueness enforcement** (pre-multi-replica): add `os.Hostname()` default + production guard before horizontal scaling.
- **TT-Cache-1 / TT-Cache-4 / TT-Cache-5 / TT-Cache-6** ([roadmap](docs/post_phase2_roadmap.md)) — only relevant if D8 demo expands to multi-ticket-type-per-event.
- **D4.3** (deferred from D4.2 PR runbook) — dual-secret webhook rotation for zero-downtime rotation.
- **O3.2 bare-metal benchmark** (parked) — find the real ceiling on dedicated hardware.

## [1.1.0] — 2026-05-18 — Stage 5 durable intake + saga idempotency fence + shutdown hardening

Post-v1.0.0 production-hardening sprint. Three architectural additions (Stage 5, saga PG fence, drift detector auto-rehydrate) plus reliability fixes surfaced by a 5-agent multi-layer review (go-reviewer + silent-failure-hunter + backend-architect + two search-specialist production-alignment agents).

### Added

- **Stage 5 — durable Kafka intake ([#113](https://github.com/Leon180/booking_monitor/pull/113); 2026-05-13)** — Fifth evolutionary stage binary (`cmd/booking-cli-stage5`). Replaces fire-and-forget Redis Stream write with a synchronous Kafka produce on the booking hot path, eliminating the Stage 4 outbox-relay connection-hold pattern that caused 100% business errors at t≈43s under 500-VU load. Durability trade-off: −38% intake throughput (8,378 → 5,139 accepted/s) and +5.7× latency (16.9 ms → 97 ms avg) for at-least-once crash-safe intake. New `k6_intake_only.js` script separates intake-ceiling measurement from full-flow measurement per the dual-scenario benchmark methodology.

- **Saga PG idempotency fence — PR-A ([#114](https://github.com/Leon180/booking_monitor/pull/114); 2026-05-14)** — `saga_compensations` table (migration 000016) stores `(compensation_id, order_id, redis_reverted_at)` inside the same UoW transaction as `MarkCompensated`. `WasRedisReverted` / `MarkRedisReverted` make the Redis revert step idempotent against Redis crashes: after a crash and drift-detector restore, pending Kafka redeliveries hit `WasRedisReverted=true` (from PG) and skip `revert.lua` instead of double-INCRBY. `RecordCompletion` uses `ON CONFLICT DO NOTHING` for Kafka-at-least-once redelivery. New `IncMarkRedisRevertedError` metric + outcome labels `already_compensated_redis_mark_failed` / `compensated_redis_mark_failed`. 7 unit test cases + 3 integration cases covering the full idempotency matrix.

- **Gap B drift detector auto-rehydrate + `cache_high` alert split ([#115](https://github.com/Leon180/booking_monitor/pull/115); 2026-05-15)** — Drift detector now auto-rehydrates Redis from PG when cache is low (`cache_low_excess` path) without requiring operator intervention. `cache_high` alert split into two distinct alerts: `InventoryCacheHighExcess` (P2, investigate) vs `InventoryCacheHighExcessCritical` (P0, immediate action — indicates double-INCRBY or phantom revert). Removed SETNX guard from drift detector so post-crash Redis starts at 0; pending saga INCRBYs accumulate from 0 rather than being blocked, preventing the cache_high double-count scenario. Bilingual docs updated (§2 metric inventory + §5 alert catalog).

- **5-stage breaking-point benchmark ([#117](https://github.com/Leon180/booking_monitor/pull/117); 2026-05-18)** — `docs/benchmarks/comparisons/20260513_141854_5stage_c500_d60s/` — full 10-raw-output dataset (5 stages × 2 scenarios) with `comparison.md` documenting three breaking points: BP-1 worker queue backpressure cliff (Stage 2→3, p95 51ms→1,120ms), BP-2 Stage 4 hard crash (t≈43s, 100% business errors), BP-3 Stage 5 durability tax (−38% intake throughput). Extends the 4-stage harness from PR #109 to 5 stages.

- **`redis_pel_recovery_failures_total` metric ([#118](https://github.com/Leon180/booking_monitor/pull/118); 2026-05-18)** — New Prometheus counter on startup PEL recovery failure (`processPending` error). Previously log-and-continue with no observable signal; non-zero rate now indicates inventory drift from a previous crash.

### Fixed

- **CI integration test timeout ([#116](https://github.com/Leon180/booking_monitor/pull/116); 2026-05-15)** — `go test -timeout` raised 5m→9m, job `timeout-minutes` raised 10→15. Suite grew to ~75 top-level tests after PR-A's `saga_compensation_test.go`; each boots its own `postgres:15-alpine` container (~3-5s), pushing wall-clock past the original 5m limit sized for ~43 tests. Long-term fix (one-container-per-package via `TestMain`) tracked as a follow-up PR.

- **Shutdown spurious DLQ routing — C2 ([#118](https://github.com/Leon180/booking_monitor/pull/118); 2026-05-18)** — `processWithRetry` now returns `errShutdownDuringBackoff` sentinel (not `ctx.Err()`) when graceful shutdown cancels a retry backoff sleep. Both `Subscribe` and `processPending` gate on `errors.Is(err, errShutdownDuringBackoff)` to leave the message in PEL instead of routing to DLQ + reverting inventory. Previously a pod receiving SIGTERM mid-retry would spuriously revert inventory and dead-letter messages that were merely interrupted, not exhausted.

- **Saga commit offset on graceful shutdown — C3 ([#118](https://github.com/Leon180/booking_monitor/pull/118); 2026-05-18)** — `SagaConsumer.commitOrLog` now uses `context.WithTimeout(context.Background(), 2s)` instead of the cancellable loop ctx, mirroring `IntakeConsumer.commitOrLog`. Previously the offset commit failed on shutdown, causing Kafka to redeliver already-compensated messages on the next pod start.

- **`SagaConsumer` fetch-error backoff ctx — agent review ([#118](https://github.com/Leon180/booking_monitor/pull/118); 2026-05-18)** — `time.Sleep(time.Second)` in the fetch-error path replaced with `select { case <-ctx.Done() / case <-time.After }`, matching `IntakeConsumer`. Prevents a 1s shutdown delay when the broker is unreachable at SIGTERM time.

### Compare

- v1.0.0…v1.1.0: https://github.com/Leon180/booking_monitor/compare/v1.0.0...v1.1.0

## [1.0.0] — 2026-05-09 — Phase 3 complete (D4.2 + D9/D10-minimal + D12 + D14 part 4 + D15 part 4)

Closes the Phase 3 portfolio arc started 2026-04-30. Pattern A is end-to-end against a real payment provider, the architectural-evolution comparison harness ships across four binaries (Stage 4 = the existing `cmd/booking-cli` full-stack binary, no rename per the roadmap's "just add the version label" choice), and the portfolio narrative closes with the 4th blog post + post-merge polish on the README mermaid diagrams + asciinema walkthrough.

The roadmap's v1.0.0 anchor was "D1-D16 complete" — D1-D6 shipped in v0.5.0, D7+D8-minimal+D16 shipped in v0.6.0, this release brings the remaining D-items together. (D11 + D13 were [explicitly dropped](docs/post_phase2_roadmap.md) during the 2026-05-03 scope decision — they are NOT silent gaps.)

### Added

- **D4.2 — real Stripe SDK adapter ([#110](https://github.com/Leon180/booking_monitor/pull/110); 2026-05-09)** — Replaced the in-process mock with a real `stripe-go v82.5.1` adapter at [`internal/infrastructure/payment/stripe_gateway.go`](internal/infrastructure/payment/stripe_gateway.go), selected via `PAYMENT_PROVIDER=stripe`. Mock stays in-tree for `make stress-k6` + unit tests. New `domain.ErrPayment*` sentinel taxonomy (Declined / Transient / Misconfigured / Invalid) wraps `*stripe.Error` types via `errors.Join`. Webhook verifier delegated to `stripewebhook.ValidatePayloadWithTolerance` while preserving our typed sentinels via `mapStripeWebhookError`. Two new metrics — `stripe_api_calls_total{op,outcome}` + `stripe_api_duration_seconds{op}` (`ExponentialBuckets(0.01, 2, 14)` covering 10ms→81.92s). Bilingual docs + runbook section (PCI SAQ A scope statement, sandbox→live cutover checklist, webhook secret rotation playbook, leaked-key incident response). 4 rounds of multi-agent review actioned (1 HIGH + 7 MEDIUMs + 7 LOWs); live smoke against Stripe test API validated end-to-end (real PaymentIntent minted, idempotency verified, signed webhook accepted).

- **D12 — 4-version comparison harness ([#104](https://github.com/Leon180/booking_monitor/pull/104) → [#109](https://github.com/Leon180/booking_monitor/pull/109); 2026-05-08/09)** — Senior-interview architectural-evolution talking point. Three new binaries — `cmd/booking-cli-stage1` (sync `SELECT FOR UPDATE`), `cmd/booking-cli-stage2` (Redis Lua + sync DB write), `cmd/booking-cli-stage3` (Redis Lua + async worker) — share the same `internal/` packages with the existing `cmd/booking-cli` (Stage 4 = full stack: outbox + saga). Stage 1 handlers extracted to `internal/infrastructure/api/stagehttp/` so Stages 2/3 reuse without copy-paste ([#105](https://github.com/Leon180/booking_monitor/pull/105)). Stage 4 gained saga compensator end-to-end loop duration histogram + consumer lag-since-write gauge + 13-outcome `saga_compensator_events_processed_total` taxonomy ([#108](https://github.com/Leon180/booking_monitor/pull/108)). Multi-target k6 harness + auto-generated `comparison.md` per run at `docs/benchmarks/comparisons/<TS>_4stage_c500_d60s/comparison.md` ([#109](https://github.com/Leon180/booking_monitor/pull/109)) reports BOTH `http_reqs/s` (capacity at load-shed gate) and `accepted_bookings/s` (booking hot path) per Ticketmaster / Stripe / Shopify funnel-stage decomposition. Stage 1 row-lock plateau ~1,643/s; Stages 2-4 cluster ~8,400/s.

- **D9-minimal + D10-minimal — demo polish ([#102](https://github.com/Leon180/booking_monitor/pull/102), [#103](https://github.com/Leon180/booking_monitor/pull/103); 2026-05-08)** — k6 scenario for the two-step reservation+payment flow at [`scripts/k6_two_step_flow.js`](scripts/k6_two_step_flow.js); baseline capture under [`docs/benchmarks/20260509_014318_two_step_baseline_c100_d90s/`](docs/benchmarks/20260509_014318_two_step_baseline_c100_d90s/). Asciinema terminal walkthrough at [`docs/demo/walkthrough.cast`](docs/demo/walkthrough.cast) (~2-3 minutes: `make demo-up` → curl reservation → curl payment → curl `/api/v1/orders/:id` showing state transitions). Embedded link in README.

- **D14 — README mermaid refresh ([#100](https://github.com/Leon180/booking_monitor/pull/100); 2026-05-08)** — Pattern A label refresh + cross-link from PROJECT_SPEC. README contains 6 mermaid blocks total (booking flow / saga loop / outbox path / Redis hot path / state machine / sequence diagram).

- **D15 part 4 — engineering blog ([#101](https://github.com/Leon180/booking_monitor/pull/101); 2026-05-09)** — Closing post in the bilingual (EN + zh-TW) blog series: [`2026-05-saga-pure-forward-recovery.md`](docs/blog/2026-05-saga-pure-forward-recovery.md) — "saga shouldn't manage the happy path; D7 narrowing as Garcia-Molina 1987 §5's engineering implementation". The earlier 3 posts (parts 1-3) shipped during the v0.5.0 development window via PRs [#81](https://github.com/Leon180/booking_monitor/pull/81), [#82](https://github.com/Leon180/booking_monitor/pull/82), [#83](https://github.com/Leon180/booking_monitor/pull/83) but were never explicitly credited in a release entry; they're listed below for completeness:
  - [`2026-05-cache-truth-architecture.md`](docs/blog/2026-05-cache-truth-architecture.md) ([#81](https://github.com/Leon180/booking_monitor/pull/81); 2026-05-03) — the FLUSHALL incident → 411-of-1000 silent message loss → cache-truth contract evolution.
  - [`2026-05-lua-single-thread-ceiling.md`](docs/blog/2026-05-lua-single-thread-ceiling.md) ([#82](https://github.com/Leon180/booking_monitor/pull/82); 2026-05-03) — single-key Lua serialization as the physics ceiling at 8,330 acc/s, found via VU scaling.
  - [`2026-05-detect-but-dont-fix.md`](docs/blog/2026-05-detect-but-dont-fix.md) ([#83](https://github.com/Leon180/booking_monitor/pull/83); 2026-05-03) — drift detection without auto-correction; the operational discipline trade-off.

### Changed

- **`PaymentStatusReader.GetStatus` interface** ([#110](https://github.com/Leon180/booking_monitor/pull/110)) — `(ctx, orderID uuid.UUID)` → `(ctx, paymentIntentID string)`. Breaking for any external implementer of the port (none in-tree; reconciler is the only caller). Necessary because Stripe's API has no orderID concept — the prior shape forced the adapter to hold a repository, violating the layer rule. Reconciler now reads persisted `payment_intent_id` from order row, threads it through directly, and null-guards `strings.TrimSpace == ""` (orphan case emits `recon_null_intent_id_skipped_total` instead of calling Stripe with empty string).
- **`StuckCharging` struct** ([#110](https://github.com/Leon180/booking_monitor/pull/110)) — gained `PaymentIntentID string` field. Repository scan widened to `COALESCE(payment_intent_id, '')`.
- **Production-mode config validation** ([#110](https://github.com/Leon180/booking_monitor/pull/110)) — rejects `PAYMENT_PROVIDER=mock` AND `*_test_*` keys AND `pk_live_*` (publishable key). Cross-field guard refuses `mock` provider whenever Stripe credentials are present, regardless of `APP_ENV` (catches the silent "real keys but wrong provider switch" misconfig in staging).
- **Note on planned env-var name** — the roadmap's planned `PAYMENT_GATEWAY_MODE` rendered as `PAYMENT_PROVIDER` during implementation. Selection semantics unchanged.

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
