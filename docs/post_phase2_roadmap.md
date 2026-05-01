# Post-Phase-2 Roadmap

Forward-looking sprint plan, written 2026-04-30 immediately after the Phase 2 checkpoint review. Supersedes prior memory-only roadmaps for sequencing decisions. Historical architecture evolution lives in [`scaling_roadmap.md`](scaling_roadmap.md) and is not duplicated here.

## Where we are

- **Phase 2 done**: PRs #45 (charging two-phase intent log + reconciler) → #46 (streams obs + DLQ MINID) → #47 (response shape + GET /orders/:id) → #48 (N4 idempotency fingerprint) → #49 (A5 saga watchdog + checkpoint framework).
- **First project-review checkpoint completed**: [`docs/checkpoints/20260430-phase2-review.md`](checkpoints/20260430-phase2-review.md). Grade A−. Findings split between a focused cleanup PR (Critical + cheap-Important) and 8 deferred follow-up PRs.
- **Next user goal**: make the project **demo-able** as a real e-commerce flash-sale flow. User has chosen **Pattern A** (Stripe Checkout / KKTIX style: split `POST /book` → `POST /orders/:id/pay` with reservation TTL + webhook receiver). See backlog §13.1.

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
  CP4  testcontainers integration suite for repositories.go (row 12 = roadmap N8)
  CP5  docs/runbooks/* + alerts runbook_url annotations (row 13 = roadmap N3)
  CP6  Alertmanager deployment + delivery target wiring (row 14 = roadmap N3)
  CP7  test-surface sprint: saga_compensator, handler coverage, outbox.Run (row 16)
  CP8  header-bearing N4 benchmark (row 17) — DONE in PR #59 (2026-05-01). See [`docs/benchmarks/20260501_175422_compare_c500/comparison.md`](benchmarks/20260501_175422_compare_c500/comparison.md). Result: N4 fingerprint path costs ≈18% RPS / ≈46% p95 vs no-header baseline at 500 VUs (cold-path: SETNX + SHA-256 fingerprint + payload-write — two extra Redis round-trips dominate). Materially higher than the pre-N4 estimate of "<2% RPS / 2-5% p95"; calibration captured for capacity planning. Bundled with the k6_comparison.js 200→202 contract fix surfaced by the prior CP3b no-regression run.
  CP9  Grafana dashboard panels for recon / saga / DLQ / DB pool / cache (row 18)

Phase 3 (demo readiness, ~9–13 wk total) — Pattern A + live mission control + 4-version comparison

  3a. Pattern A core (~3–5 wk)
  D1   schema migration: add orders.reserved_until + orders.payment_intent_id; new status `awaiting_payment`
  D2   domain state machine: pending → awaiting_payment → paid | expired | failed
  D3   POST /api/v1/book becomes a reservation (TTL 10–15 min); response includes payment_intent metadata
  D4   POST /api/v1/orders/:id/pay creates the PaymentIntent against the gateway adapter (Stripe-like)
  D5   POST /webhook/payment receives async outcome; webhook signature verification; idempotent against payment_intent_id
  D6   reservation expiry sweeper (mirrors A5 watchdog shape): scan `awaiting_payment` past `reserved_until`, transition → expired, revert Redis inventory via revert.lua
  D7   payment_worker stops being a saga consumer for the happy path. Saga compensator scope narrows to {expired, payment_failed}.

  3b. Demo polish (~6–8 wk; D12 runs in parallel with 3a)
  D8   frontend bootstrap: `web/` workspace (Next.js + Stripe Elements + MockStripeAdapter for credential-free demo)
  D9   load-test refresh: k6 scenario script for the two-step flow; baseline capture
  D10  demo recording: ~5-minute video walkthrough (now richer because D11/D13 are in scope) — mission-control view of normal flow → push spike load → watch Stage 1 collapse → same load on Stage 4 → reservation/payment/webhook flow → expiry sweep → saga compensation example

  D11  live mission-control dashboard (~1–2 wk)
       Backend: `GET /api/v1/admin/stream` (SSE). Polls own `/metrics` every 250ms, diffs, pushes deltas to all subscribers + initial-snapshot on connect. Gated on `ENABLE_ADMIN_DEMO=true` (separate from `ENABLE_PPROF`; off in prod).
       Frontend (in `web/` workspace from D8): live counters (inventory remaining, orders by status, stream depth, DLQ count, watchdog/recon recent activity, p99 sliding window) + **animated pipeline diagram** built with React Flow — orders rendered as dots flowing Redis → worker → DB → outbox → Kafka → payment → confirmed/failed. The animation IS the demo's killer visual.

  D12  4-version multi-cmd comparison harness (~4 wk; can start in parallel with 3a)
       Same `internal/` packages, different fx wirings under separate `cmd/` entries — Clean Architecture as the answer:
       - `cmd/booking-cli-stage1/` — API → Postgres `SELECT FOR UPDATE`. No Redis, no Kafka, no async, no saga. Pure synchronous baseline. Estimated 30 LOC main.go.
       - `cmd/booking-cli-stage2/` — API → Redis Lua atomic deduct → SYNCHRONOUS DB write. Inventory in Redis but no async buffering.
       - `cmd/booking-cli-stage3/` — API → Redis Lua → `orders:stream` → worker → DB. Async + worker pool, but no event-driven downstream (no Kafka outbox, no payment service, no saga).
       - `cmd/booking-cli-stage4/` — current `cmd/booking-cli/` as canonical Stage 4. No rename; just add the version label.
       Each binary registers Prometheus default labels with constant `version_tag={stage1..stage4}` so Grafana can split by version. Each implements only `POST /api/v1/book` for the comparison; full feature set stays on Stage 4. `docker-compose` gets a `comparison` profile spinning up all four on ports 8081–8084. Shared Postgres + Redis instances with namespace isolation (per-stage Postgres database, per-stage Redis DB index). k6 takes `--target=http://localhost:8081` argument; results stored under `docs/benchmarks/comparisons/<timestamp>/{stage1,stage2,stage3,stage4}/`.

  D13  frontend comparison view (~1–2 wk; depends on D12)
       Reads benchmark archive JSON from `docs/benchmarks/comparisons/`. Side-by-side time-series charts (RPS over time, p99 latency, error rate, conversion) with Recharts/Chart.js. Annotated callouts: "Stage 1 collapses at 4K RPS due to PG row-lock contention", "Stage 2 stops scaling at 11K RPS because synchronous DB write becomes the bottleneck", etc. Optional `Run benchmark now` button: POSTs to `/api/v1/admin/benchmark?stage=N&duration=60s` → backend triggers k6 against the selected stage → streams progress via the same SSE channel as D11.

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
