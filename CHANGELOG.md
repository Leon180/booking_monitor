# Changelog

All notable architectural milestones in this project, written in reverse chronological order. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This is a portfolio / learning project, not a published library — versions mark **architecture inflection points**, not API stability promises. Use the GitHub Releases page (https://github.com/Leon180/booking_monitor/releases) for the rendered timeline; this file is the authoritative source.

## [Unreleased] — Phase 3 (Pattern A + 4-version comparison + portfolio narrative)

Planned per [`docs/post_phase2_roadmap.md`](docs/post_phase2_roadmap.md). High-level scope:

- **Pattern A core (D1-D7)**: split `POST /book` into reservation + `POST /pay` + `POST /webhook/payment`, reservation TTL expiry sweeper, saga compensator scope narrowed to `{expired, payment_failed}`. Brings the booking flow into Stripe Checkout / KKTIX shape.
- **D12 4-version comparison harness**: `cmd/booking-cli-stage{1,2,3,4}` binaries against the same `internal/` packages. Markdown comparison report per benchmark run; this is the senior-interview architectural-evolution talking point.
- **Minimal demo polish (D8-minimal, D9-minimal, D10-minimal)**: single Stripe Elements page, k6 scenario for two-step flow, asciinema terminal walkthrough. NO admin dashboard, NO React Flow animation.
- **Portfolio narrative (D14, D15, D16)**: README mermaid architecture diagrams, multi-post `docs/blog/` series with hybrid-STAR template, this CHANGELOG + retroactive tags + GitHub Releases page.

Will land as v0.5.0 when Pattern A is complete, v1.0.0 when all of Phase 3 is complete.

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
