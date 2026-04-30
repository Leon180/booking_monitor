# Checkpoint Review: 2026-04-30 — Phase 2 boundary (post A5)

> First checkpoint. Triggered by Phase 2 completion (5 PRs landed: #45 charging two-phase intent log, #46 streams obs/DLQ, #47 response shape + GET /orders/:id, #48 N4 idempotency fingerprint, #49 A5 saga watchdog + checkpoint framework). Framework: [`.claude/skills/project-review-checkpoint/SKILL.md`](../../.claude/skills/project-review-checkpoint/SKILL.md).

## Pre-check snapshot

- **Trigger**: Phase boundary (Phase 2: charging two-phase + reliability sweep)
- **PRs since last checkpoint**: 5 (#45..#49) — first checkpoint, so this also subsumes everything since project start retroactively
- **Test coverage** (per package):
  | Package | Coverage |
  | :-- | --: |
  | `internal/application` | 34.6% |
  | `internal/application/payment` | 65.4% |
  | `internal/application/recon` | 90.2% |
  | `internal/application/saga` | 93.5% |
  | `internal/domain` | 92.6% |
  | `internal/infrastructure/api/booking` | 31.0% |
  | `internal/infrastructure/api/dto` | 100.0% |
  | `internal/infrastructure/api/middleware` | 79.1% |
  | `internal/infrastructure/api/ops` | 72.7% |
  | `internal/infrastructure/cache` | 56.8% (90s test runtime — slow) |
  | `internal/infrastructure/config` | 57.4% |
  | `internal/infrastructure/observability` | 32.6% |
  | `internal/infrastructure/payment` | 51.9% |
  | `internal/infrastructure/persistence/postgres` | **1.3%** ← critical gap |
  | `internal/log` | 75.2% |
  | `internal/log/tag` | 100.0% |
- **Lint state**: `golangci-lint run ./...` → **0 issues**
- **Stack health**: all 11 services up; app/postgres/redis/kafka/zookeeper report `(healthy)`
- **Reviewers dispatched** (8, in parallel): code-reviewer × 6 (architecture, tests, docs, ops, security, defensibility), Explore (debt), general-purpose (perf)

Note on findings: agent claims were spot-checked against code before promotion to "Critical". Two checks materially changed the report:
1. **Refuted**: D8's claim that `revert.lua` is non-atomic (EXISTS-then-SET race). Code comment in [`deploy/redis/revert.lua`](../../deploy/redis/revert.lua) explicitly defends atomicity via Redis Lua single-threaded execution. Finding dropped.
2. **Confirmed**: D8's claim that the reconciler's max-age force-fail path skips Kafka emission. `MarkFailed` only transitions state; outbox emission lives in `PaymentService.ProcessOrder`, not in `recon`. Real correctness gap → promoted to Critical.

---

## Findings by dimension

### 1. Architecture coherence

**Critical**
- **A1 — Application packages import infrastructure**. [`internal/application/saga/watchdog.go:49-50`](../../internal/application/saga/watchdog.go) and [`internal/application/recon/reconciler.go:39-40`](../../internal/application/recon/reconciler.go) import `infrastructure/config` (yaml/env-tagged structs) and `infrastructure/observability` (Prometheus singletons). Inverts the dependency rule: application must depend only on domain. Fix: define plain-Go `Config` + `Metrics` interfaces inside each application sub-package; bootstrap maps `cfg.Saga.* → saga.Config{...}` and provides `prometheusSagaMetrics` adapters. Same shape as the existing `BookingMetrics` / `WorkerMetrics` precedent. Effort: M.

**Important**
- **A2 — `observability/metrics.go` is a 593-line god-file**. Mixes HTTP Gin middleware (couples observability → gin), worker counters, recon vars, saga vars, and idempotency counters. Split into `metrics_{http,worker,recon,saga,cache}.go`; move `MetricsMiddleware` to `infrastructure/api/middleware/`. Decouples the gin import from the observability layer. Effort: S–M.
- **A3 — `OrderFailedEvent` synthesised with `Version: 0`**. [`internal/application/saga/watchdog.go:216-221`](../../internal/application/saga/watchdog.go) constructs a partial `OrderCreatedEvent` (zeroed `Amount`/`Status`/`CreatedAt`/`Version`) just to feed `NewOrderFailedEvent`. The resulting event has `Version: 0` instead of `OrderEventVersion = 2` — a wire-format schema violation. Fix: add `NewOrderFailedEventFromOrder(order, reason)` factory; remove the throwaway intermediate. Effort: S.
- **A4 — `HandleViewEvent` is a stub with side effects**. [`internal/infrastructure/api/booking/handler.go:241-245`](../../internal/infrastructure/api/booking/handler.go) increments `page_views_total{page="event_detail"}` but returns `gin.H{"message": "View event"}` instead of querying the event. The metric goes non-zero on real traffic, misleading operators; the route signals "implemented" while it isn't. Either implement properly (call `eventService.GetEvent` → typed DTO) or drop the route + metric until ready. Effort: S.

**Nice-to-have**
- `StuckCharging` and `StuckFailed` are identical structs — type alias would express intent more cleanly until they diverge.
- `internal/infrastructure/persistence/postgres/repositories.go` is 532 lines (under the 800 cap); split per aggregate when it next grows.

### 2. Test surface health

**Critical**
- **T1 — `infrastructure/persistence/postgres` at 1.3% coverage**. The recon-critical SQL (`transitionStatus`, `FindStuckCharging`, `FindStuckFailed`, `MarkCharging`, `MarkConfirmed`, `MarkFailed`, `DecrementTicket`/`IncrementTicket`, `ListOrders`) has zero direct tests at any layer ([`repositories.go:348-465`](../../internal/infrastructure/persistence/postgres/repositories.go)). The application-layer recon (90.2%) and watchdog (93.5%) tests run against mocks only — the actual SQL has never been validated. A wrong predicate or wrong column order in `FindStuckFailed` would silently fail CI. Highest-risk silent-failure surface in the codebase.

**Important**
- **T2 — `application/saga_compensator.go` has no test**. Watchdog tests stub it with `fakeCompensator`; the real Redis `revert.lua` invocation + DB rollback path is untested. Could be tested with miniredis + sqlmock.
- **T3 — Unbalanced handler coverage**. `HandleListBookings`, `HandleCreateEvent`, `HandleViewEvent` have zero tests. `HandleBook` has 2 cases — missing invalid-body, missing-fields, replay-at-handler-boundary cases.
- **T4 — `outbox_relay.go.Run` loop untested**. Only `processBatch` covered. Leader election (advisory lock), context cancellation, batch polling are uncovered.
- **T5 — `application/event_service.go.CreateEvent` has zero tests**. Write path against both event repo and inventory repo, called from a public handler.

**Nice-to-have**
- `MetricsMiddleware`, `DBPoolCollector`, `RegisterRuntimeMetrics` untested — Prometheus startup misregistration would only surface at `docker-compose up`.
- `internal/infrastructure/config.Load()` (YAML/env parse) untested.
- No `t.Parallel()` anywhere; cache test takes 90s, parallelising would recover ~60s.
- No integration / chaos / E2E layer at all (deferred N8).

### 3. Documentation drift

**Critical**
- **D1 — Data flow table claims 5xx responses are cached, contradicts both code and §5 of the same doc**. [`docs/PROJECT_SPEC.md:50`](../PROJECT_SPEC.md) (and identically at [`PROJECT_SPEC.zh-TW.md:46`](../PROJECT_SPEC.zh-TW.md)) reads "Only 2xx + 5xx responses get cached on the way out". The actual code at [`internal/infrastructure/api/middleware/idempotency.go:126-127`](../../internal/infrastructure/api/middleware/idempotency.go) caches only `status >= 200 && status < 300`. The §5 contract table in the same doc correctly says "Only 2xx is cached". A client-SDK developer reading the data flow table first would build the wrong retry policy. Bilingual contract correctly cloned the error to zh-TW — both must be fixed in the same response.

**Important**
- **D2 — CLAUDE.md "Current State" frozen at PR #23 (Apr 24)**. PRs #24–#49 missing — A4/A5/N4/k8s probes/audit log/order_status_history/recon subcommand all undocumented in the quick-reference. Date-stamp says 2026-04-24, today is 2026-04-30.
- **D3 — Nine new env vars missing from CLAUDE.md Key Env Vars**: `RECON_SWEEP_INTERVAL`, `RECON_CHARGING_THRESHOLD`, `RECON_GATEWAY_TIMEOUT`, `RECON_MAX_CHARGING_AGE`, `RECON_BATCH_SIZE`, `SAGA_WATCHDOG_INTERVAL`, `SAGA_STUCK_THRESHOLD`, `SAGA_MAX_FAILED_AGE`, `SAGA_BATCH_SIZE`. These are exactly the knobs an operator would reach for during a `ReconStuckCharging` or `SagaStuckFailedOrders` page.
- **D4 — Memory `post_roadmap_pr_plan.md` lists A4/A5 as future "Phase 2" work** despite both merged. Any future agent session reading this for "what's next" will re-propose them.
- **D5 — Memory `project_state.md` claims Go 1.24** (actual is 1.25 per `go.mod` `toolchain go1.25.9`); cuts off at PR #23.
- **D6 — Data flow happy path omits `Pending → Charging` transition** added in A4 (PR #45). Reader following the table-form data flow won't realise `Charging` is on the hot path; only `§6.7` covers it.

**Nice-to-have**
- `docs/architecture/current_monolith.md` Mermaid diagram has no `Reconciler` or `SagaWatchdog` nodes.
- Bilingual structural parity verified — section count + table row count match across all 4 pairs (CLAUDE, README, PROJECT_SPEC, monitoring). The contract is working at the structural level.

### 4. Operational maturity

**Critical**
- **O1 — `docs/runbooks/` does not exist; no `runbook_url` on any of 18 alerts**. `grep -c "runbook_url" deploy/prometheus/alerts.yml` = 0. On-call gets a Prometheus alert with no triage link, no escalation path. Six critical-severity alerts (`HighErrorRate`, `ReconFindStuckErrors`, `ReconMaxAgeExceeded`, `SagaWatchdogFindStuckErrors`, `SagaMaxFailedAgeExceeded`, `RedisStreamCollectorDown`) fire without a documented response. This was tracked as roadmap N3 — escalating to Critical because the alerts shipped in this checkpoint window.
- **O2 — No Alertmanager configured**. `deploy/` has no Alertmanager manifest. Prometheus evaluates rules and transitions to Firing state but has no delivery (no Slack / PagerDuty / webhook). All alerts are permanently silent to humans. Highest on-call pain point.
- **O3 — `payment_worker` and `recon` containers are not scraped by Prometheus**. [`deploy/prometheus/prometheus.yml`](../../deploy/prometheus/prometheus.yml) only scrapes `app:8080`, `prometheus:9090`, `jaeger:14269`. The `recon_*` family (8 metrics added in PR #45), `kafka_consumer_retry_total` from the payment consumer, and `saga_poison_messages_total` are registered via `promauto` into the per-process `prometheus.DefaultRegisterer` of the worker processes. Their `/metrics` endpoint is never pulled, so the `ReconStuckCharging`, `ReconFindStuckErrors`, `ReconGatewayErrors`, `ReconMaxAgeExceeded` alerts watch metrics that are dark in the compose stack today. Verified: both containers expose 8080/tcp internally but compose has no `ports:` mapping for either, AND no Prometheus scrape job lists them.

**Important**
- **O4 — `idempotency_cache_get_errors_total` documented as page-worthy but no alert exists**. `metrics.go:216` and `monitoring.md:81` both describe a "rate > 0 for 1m → page" alert; `alerts.yml` has no such rule.
- **O5 — `recon_mark_errors_total` registered with no alert**. Sustained non-zero means orders are stuck in `Charging` because the reconciler's DB write is broken — silently invisible to on-call.
- **O6 — Four "operational red flag" counters unmonitored**: `db_rollback_failures_total`, `redis_xack_failures_total`, `redis_revert_failures_total`, `redis_xadd_failures_total`. All described in `metrics.go` docstrings as page-worthy; no alerts, no dashboard panels.
- **O7 — Provisioned Grafana dashboard frozen at 6-panel PR #41 surface**. No panels for stream backlog / DLQ depth / DB pool saturation / reconciler / saga watchdog / cache hit rate / worker throughput. An on-call opening Grafana during a recon or saga incident sees only RPS, latency, conversion, goroutines, memory.
- **O8 — `HighLatency` uses 1-minute rate window with `for: 1m`**. Aggressive — a single GC pause can flap. Should be at least 5m, consistent with the project's prior anti-flap work on `HighErrorRate`.

**Nice-to-have**
- No `docs/slo.md`; thresholds (5% error rate, 2s p99) are arbitrary, not SLO-derived. No multi-window multi-burn-rate alerts (SRE Workbook Ch.5). Tracked as roadmap N3.
- `kafkaBrokerPing` ([`ops/health.go:194-206`](../../internal/infrastructure/api/ops/health.go)) iterates brokers serially inside the readiness goroutine — can silently consume most of the 1s probe budget if the first broker is slow. Fan-out within the function would tighten worst-case.
- `saga-watchdog` subcommand has no compose service entry — runs only via manual `--once` invocations or external CRON; the alerts can't fire in dev.

### 5. Security posture

**Critical** — none.

**Important**
- **S1 — Unbounded pagination `size` parameter**. [`internal/infrastructure/api/booking/handler.go:95-98`](../../internal/infrastructure/api/booking/handler.go) floor-clamps `params.Size` to 10 but has no ceiling. `?size=1000000` → `LIMIT 1000000` → full-table scan + large allocation. Exploitable by any caller in one request, bypasses nginx rate-limit. Fix: cap at 100. Effort: trivial.
- **S2 — Status filter passes unvalidated string to SQL**. [`internal/infrastructure/api/dto/request.go:52-57`](../../internal/infrastructure/api/dto/request.go) and [`postgres/repositories.go:192-204`](../../internal/infrastructure/persistence/postgres/repositories.go). NOT a SQL injection (parameterized `$1`) but no enum validation — invalid value silently returns 0 rows instead of 400. Defense-in-depth gap if `OrderStatus` is ever used in dynamic SQL elsewhere. Add `IsValid()` method.

**Low**
- `HandleViewEvent` echoes raw URL param into JSON response (no validation, but it's a stub so the issue is the stub).
- `.env.example` ships `sslmode=disable` for Postgres — fine for local but models the wrong default for staging.
- `docker-compose.yml` publishes pprof port `"6060:6060"` to host unconditionally; harmless when `ENABLE_PPROF=false` but a footgun. Bind to `"127.0.0.1:6060:6060"` to match the in-process default.

**Govulncheck**: zero reachable vulnerabilities. One transitive `google.golang.org/grpc` advisory (`GO-2026-4762`, authorization bypass via `:path` missing leading slash) — code does not call the vulnerable symbol. No action; monitor for grpc patch.

### 6. Performance regressions

**Critical** — none.

**Important**
- **P1 — N4 (#48) shipped without a header-bearing benchmark**. The convention says hot-path PRs MUST land a comparison report; #48 has none because the standard `k6_comparison.js` doesn't send `Idempotency-Key`, so the middleware short-circuits at line 184 with a single header read (no measurable delta). Real header-bearing traffic absorbs an estimated 1–2 µs CPU + ~1 KB allocations per request (SHA-256 + canonical JSON + `captureWriter` buffer). At 500 VU this is roughly **2–5% p95 latency, <2% RPS**. Not regression-class but should be confirmed before declaring perf-stable. Fix: add `IDEMPOTENCY` flag to `k6_comparison.js` or a dedicated `k6_idempotency.js`.

**Nice-to-have**
- `captureWriter.body *bytes.Buffer` is heap-allocated per header-bearing request — `sync.Pool` candidate (same template as PR #15 used for Lua args).
- pprof harness hasn't been re-run since PR #15 (Apr 13) — 5-minute heap profile post-N4 would confirm whether the buffer alloc is hot enough to act on.

Most recent baseline: [`docs/benchmarks/20260428_225152_compare_c500_a4_charging_intent`](../benchmarks/20260428_225152_compare_c500_a4_charging_intent/) — 54,206 RPS / p95 12.63 ms. Captures #44 + #45; PRs #46 + #49 are off-hot-path; #47 + #48 are minor / opt-in. **Verdict**: defer full benchmark to Phase 3 with a note recording the N4 estimate.

### 7. Tech debt inventory

**Critical** — none.

**Important** — none above the surfacing already done by other dimensions.

**Nice-to-have**
- 0 TODO / FIXME / XXX / HACK in Go code (clean).
- 0 `t.Skip` in tests (clean).
- 3 of 5 audit-target consts still hardcoded (`outboxPollInterval`, `sagaMaxRetries`, `sagaRetryKeyTTL`) — earmarked for a "configurable runtime tunables" PR.
- All 11 migrations safe (proper IF EXISTS guards; 000008 down is intentionally destructive with documented rationale; 000010/000011 use DROP+CREATE since predicate-altering ALTER isn't supported).
- No dead code: PR #49's `Watchdog.Run` deletion has zero residual callers.

### 8. Senior-grade defensibility

> Grade: **A−**. Architecture is genuinely sophisticated for a portfolio piece. Deduction is specific: a few seams exist where the candidate clearly knows the right answer but hasn't shipped it yet, and a tough interview can press them.

**Critical correctness gap surfaced by D8 + verified against code**

- **DEF-CRIT — Reconciler max-age force-fail leaks Redis inventory**. Verified: `MarkFailed` ([`postgres/repositories.go:465`](../../internal/infrastructure/persistence/postgres/repositories.go)) only calls `transitionStatus` (writes `orders` + `order_status_history`); it does not write to `events_outbox`. The reconciler's max-age branch ([`recon/reconciler.go:173`](../../internal/application/recon/reconciler.go)) calls `MarkFailed` directly. No `order.failed` Kafka event is emitted, so `SagaCompensator` never sees the order, so `RevertInventory` is never called. Redis stock was deducted at booking time (`MarkCharging` only happens after the deduct). After force-fail, the inventory count drifts permanently below the Postgres source of truth. Under sustained gateway instability + repeated max-age fire-events, Redis inventory leaks proportionally. Fix shape: extend the max-age branch to also `outboxRepo.Create(NewOrderFailedOutbox(...))` inside the same UoW, or call the saga compensator directly. Either path closes the leak.

**Top interviewer challenges (likelihood-ranked, with honest weaknesses + defenses)**

1. *"Why dual-tier inventory? Convince me Redis is worth the operational cost."* — Defense ready: USE method analysis at 500 VU shows single-Postgres saturates inventory writes around 8K RPS; Redis Lua atomic deduct is orders-of-magnitude faster. Redis-vs-Postgres divergence window during partial failure is bounded by PEL redelivery + idempotent revert.lua. **Caveat**: revert.lua's atomicity is correctly defended by the comment block — single-threaded Lua execution + appendfsync semantics. Be ready to walk that comment.
2. *"Walk me through what happens when payment_worker crashes between MarkCharging and gateway.Charge."* — This is where DEF-CRIT bites. After fix lands, the answer is clean: reconciler verifies via `gateway.GetStatus`, ChargeStatusNotFound + age > MaxChargingAge → emit `order.failed` outbox → saga compensator reverts Redis inventory. Without the fix, max-age force-fails leak inventory.
3. *"Your `transitionStatus` does FOR UPDATE inside a CTE then a second GetByID for disambiguation. Is there a race?"* — Defensible: the second GetByID is on the not-found path only and runs in autocommit; if the row transitioned between statements, the disambiguating read returns ErrInvalidTransition correctly. Cost: extra DB round-trip on the failure path. Acceptable trade-off for clarity.
4. *"Idempotency key has 24h TTL globally, no user scoping. Replay attacker concern?"* — Acknowledge proactively: namespace today is global. Production hardening would prefix `idempotency:{user_id}:{key}`. ASCII-printable + 128-char cap mitigates obvious abuse vectors.
5. *"PaymentService is a Kafka consumer + gateway client + state-machine driver + saga initiator. SRP?"* — Honest: yes, `ProcessOrder` at 220 lines does too much. Splittable into `ChargeOrchestrator` + `PaymentEventConsumer`. Tracked.
6. *"Watchdog won't auto-transition on MaxFailedAge but reconciler WILL force-fail on MaxChargingAge. Why?"* — Watchdog comment defends asymmetry correctly: phantom-revert from Failed → Compensated is unsafe without inventory verification. **But the follow-up — "who reverts inventory after recon force-fail?" — currently has no good answer until DEF-CRIT lands.**
7. *"`order_status_history` for audit but no event sourcing — worst of both worlds?"* — Defensible: structured audit log for compliance + observability, not event replay. The hot-path read cost of full event sourcing is unaffordable at 500 VU.
8. *"Postgres tests at 1.3%. The CTE in `transitionStatus` is load-bearing for correctness. How do you know it works?"* — Honest gap: T1 above. Pre-acknowledge as deferred; testcontainers PR is roadmap (N8). Defense: deliberate trade-off for speed; SQL validated manually against the Docker stack.
9. *"`gateway.Charge` returns no charge ID. How does Stripe-real reconciliation work?"* — Acknowledge: mock papers over a real persistence gap. Production would store PaymentIntent ID on the order; Pattern A refactor (chosen post-checkpoint) addresses this.
10. *"Single Redis. Failure during peak load?"* — Tracked as A9/A10 (Sentinel + circuit breaker, k8s prerequisite).

**Strongest portfolio talking points** (don't only critique):
- CTE-based atomic `transitionStatus` with `order_status_history` INSERT in one SQL statement.
- `PaymentStatusReader` interface on the reconciler — read-only, prevents accidental double-charge in recovery code (interface segregation as correctness).
- UUIDv7 end-to-end threading with PEL retry reuse.
- `StuckCharging` vs `StuckFailed` as distinct types preventing wrong-recovery-path swap.
- N4 three-way idempotency switch (hit+match, hit+legacy, hit+mismatch) with lazy fingerprint write-back — Stripe-level API hygiene.

---

## Action plan

Triage rule: cleanup PR addresses Critical + the Important findings cheap enough to bundle. Larger Important items get their own PR. Nice-to-have defers to backlog.

| # | Finding | Severity | Effort | Target PR |
| :-- | :-- | :-- | :-- | :-- |
| 1 | DEF-CRIT — recon max-age must emit `order.failed` outbox event before MarkFailed | Critical | S–M | **Cleanup PR (must)** |
| 2 | D1 — fix "5xx are cached" data-flow row in PROJECT_SPEC EN + zh-TW | Critical | XS | Cleanup PR |
| 3 | D2/D3/D4/D5 — refresh CLAUDE.md current-state + 9 missing env vars + memory file currency | Important | S | Cleanup PR |
| 4 | O3 — add `payment_worker` + `recon` + `saga_watchdog` to `prometheus.yml` scrape jobs (verify each binary serves `/metrics` first; if not, expose it) | Critical | S | Cleanup PR |
| 5 | A3 — `NewOrderFailedEventFromOrder` factory; remove the throwaway intermediate; fix `Version: 0` bug | Important | S | Cleanup PR |
| 6 | S1 — cap pagination `size` at 100 | Important | XS | Cleanup PR |
| 7 | S2 — `OrderStatus.IsValid()` + use in `StatusFilter` | Important | XS | Cleanup PR |
| 8 | O8 — `HighLatency` rate window 1m → 5m | Important | XS | Cleanup PR |
| 9 | O4/O5/O6 — add alerts for `idempotency_cache_get_errors_total`, `recon_mark_errors_total`, the four `*_failures_total` counters | Important | S | Cleanup PR |
| 10 | A1 — extract `recon.Config` + `saga.Config` plain structs; inject `Metrics` interfaces; remove infrastructure imports from application | Critical | M | **Separate PR (architecture)** |
| 11 | A2 — split `observability/metrics.go`; move `MetricsMiddleware` to `api/middleware/` | Important | M | Separate PR (with #10) |
| 12 | T1 — testcontainers integration suite for `repositories.go` (transitionStatus, FindStuckCharging/Failed, DecrementTicket) | Critical | L | **Separate PR (=N8 roadmap item)** |
| 13 | O1 — create `docs/runbooks/<alert>.md` for 18 alerts + `runbook_url` annotation on each | Critical | M | **Separate PR (=N3 roadmap item)** |
| 14 | O2 — Alertmanager configuration (delivery target: at minimum a webhook for local; production wiring deferred) | Critical | M | **Separate PR (=N3 roadmap item)** |
| 15 | A4 — implement or remove `HandleViewEvent` stub | Important | S | Standalone tiny PR or fold into Pattern A |
| 16 | T2/T3/T4/T5 — `saga_compensator` test, handler coverage, `outbox_relay.Run` test, `event_service.CreateEvent` test | Important | M | Separate PR (test-surface sprint) |
| 17 | P1 — header-bearing k6 baseline | Important | S | Standalone benchmark PR |
| 18 | O7 — Grafana dashboard panels for recon / saga / DLQ / DB pool / cache hit rate | Important | S–M | Standalone PR |
| Backlog | All Nice-to-haves | — | — | `architectural_backlog.md` |

**Cleanup PR scope** (rows 1–9): a focused, reviewable PR with the cheap-to-bundle Critical + Important fixes. Rows 10–14 are large enough that bundling would defeat reviewability; they go to separate PRs with their own scope.

## Defensibility self-critique summary

The portfolio is **A−**. The strongest narrative is the four-layer idempotency story (API + worker + saga + gateway) with UUIDv7 threading, the CTE-atomic `transitionStatus` + `order_status_history` audit pattern, and the recon/watchdog asymmetry as a deliberate product decision. The single most damaging finding from this checkpoint is **DEF-CRIT**: the reconciler's max-age force-fail path silently leaks Redis inventory because `MarkFailed` writes to `orders` + audit log only, never to `events_outbox`, so the saga compensator is never triggered. This is real correctness debt, not a hypothetical edge case, and is what a senior interviewer would press on first under the "walk me through a payment_worker crash" challenge. The rest of the deductions (no auth — explicitly scoped, postgres test coverage 1.3% — deferred N8, no Alertmanager — deferred N3, layer violations in saga/recon — recent regression introduced in PRs #45/#49) are defensible-with-honest-acknowledgement provided the cleanup PR ships before any external share.

## Outcome

_TBD — link to cleanup PR here once it merges and note resolution._
