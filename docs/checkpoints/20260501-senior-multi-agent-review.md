# Checkpoint Review: 2026-05-01 — Senior multi-agent review

## Pre-check snapshot

- **Trigger**: User-requested senior-level whole-project review; pre-portfolio style emphasis.
- **Branch**: `feat/cp4c-migration-roundtrip` at `7cb099f`.
- **PRs since last checkpoint**: roughly #50..#64 plus current CP4c migration-roundtrip fixups.
- **Test state**: `go test ./...` passed with host loopback permission. Sandboxed run failed on tests that bind `127.0.0.1:0` (`metrics_listener`, `miniredis`), which is environment noise rather than a code failure.
- **Coverage state**: `go test -cover ./internal/...` produced useful package coverage for tested packages but exited non-zero because the coverage path hit a local Go toolchain mismatch (`go1.25.9` package archive vs `go1.25.1` tool) in packages with no tests / generated mocks. Notable low package figures: `internal/application/booking` 23.1%, `internal/bootstrap` 23.8%, `internal/infrastructure/observability` 35.2%, `internal/infrastructure/persistence/postgres` 1.3% (misleading because the main Postgres coverage lives in external integration tests).
- **Lint / vuln state**: `env GOCACHE=/private/tmp/booking_monitor_gocache golangci-lint run -v` completed with `0 issues`; default cache invocation failed package loading locally. `govulncheck ./...` found 0 reachable vulnerabilities and 1 imported-but-not-called vulnerability.
- **Stack health**: Docker compose stack is up. App, payment worker, recon, saga watchdog, Redis, Kafka, and ZooKeeper report healthy; Prometheus, Grafana, Jaeger, nginx, Alertmanager, and Postgres are running.
- **Reviewers dispatched**: Architecture, docs, ops, performance, tech debt, and defensibility agents completed. Test and security agents stalled and were shut down; those dimensions were synthesized locally from tests, coverage, lint, route/security inspection, and `govulncheck`.

## Findings by dimension

### 1. Architecture coherence

**Critical**: None. Directional dependencies are broadly healthy: domain does not import application/infrastructure, and application packages do not import infrastructure.

**Important**:
- Domain is carrying transport and process contracts: `IdempotencyResult` describes HTTP replay data, `InventoryRepository.DeductInventory` documents Redis stream publishing, and outbox event vocabulary lives in domain. See `internal/domain/idempotency.go`, `internal/domain/inventory.go`, and `internal/domain/event.go`.
- The top-level `internal/application` package mixes dependency-free contracts with an Fx module that imports child packages. This is already creating import-cycle pressure visible in `cmd/booking-cli/server.go`.
- Domain repository interfaces expose persistence/read-model shapes such as `GetByIDForUpdate`, `FindStuckCharging`, and `FindStuckFailed`.
- `payment.Service` accepts full `PaymentGateway` despite the narrower `PaymentCharger` port already existing.

**Nice-to-have**:
- Application tracing imports OTel directly while metrics are abstracted; either document this as an allowed cross-cutting dependency or mirror the metrics port pattern.
- `redisOrderQueue` owns subscription, PEL recovery, retry, compensation, DLQ writing, metrics, and parsing. Split when next touched.
- Postgres repositories are still bundled in one large file; splitting by aggregate would reduce merge and ownership friction.

**Grade**: B+.

### 2. Test surface health

**Critical**: None. The suite passes end-to-end when allowed to bind loopback ports.

**Important**:
- Package coverage is uneven. Strong packages include saga/recon/worker/domain, but booking service, bootstrap, observability, and Postgres adapter package-level coverage are weak or misleading.
- The coverage command itself is not a stable local signal right now because of the Go toolchain mismatch in coverage mode. CI may still be fine, but local checkpoint evidence is degraded.
- Kafka messaging packages have no local tests in-package, and several async/consumer paths are still more incident-tested than systematically model-tested.
- Some tests still use sleeps (`outbox/relay_test.go`, `metrics_listener_test.go`, health checks). Most are bounded, but they remain flake candidates under slow hosts.

**Nice-to-have**:
- CP4 integration tests materially improve migration/repository confidence. Keep expanding integration coverage around outbox relay, Kafka consumers, and worker drain behavior rather than only repository CRUD.

**Grade**: B.

### 3. Documentation drift

**Critical**:
- The agent-instruction bilingual pair is broken at the contract source. The contract names `.Codex/AGENTS.md` and `.Codex/Codex.zh-TW.md`, but the active instructions are at root `AGENTS.md`; the hook still watches stale `.claude/CLAUDE.*` paths.
- Database docs are materially stale: `PROJECT_SPEC` still describes the older three-table / 7-migration / serial-id shape, while migrations now include UUID PK migration, `order_status_history`, charging intent updates, and failed-order index changes.

**Important**:
- `PROJECT_SPEC` file-reference tables point to pre-split application and observability files.
- README advertises `GET /api/v1/events/:id` as event details, but the handler returns a stub.
- Checkpoint/skill docs still reference `.claude` / `.Codex` paths while current skills live under `.agents/skills`.
- Prior checkpoint outcome/history is stale despite several cleanup PRs landing.

**Nice-to-have**:
- Existing README / PROJECT_SPEC / monitoring bilingual pairs are structurally aligned; this is content drift, not EN/zh-TW structure drift.

**Grade**: B-.

### 4. Operational maturity

**Critical**:
- Alerts still do not leave the box. Prometheus is wired to Alertmanager, but severity routes ultimately send to `receiver: null`, so critical alerts do not page by default.
- Two critical runbooks contain SQL that will fail against `order_status_history` by querying `created_at` / `status` instead of the actual `occurred_at`, `from_status`, and `to_status` schema.

**Important**:
- There is no explicit SLO or error-budget model; current alerts are fixed-threshold, not burn-rate based.
- Prometheus scrape coverage is application-heavy and infra-light: no Postgres, Redis, Kafka, nginx, node/cAdvisor, or blackbox exporter coverage.
- No target-down alert covers scrape failures, so worker metric listeners can disappear silently.
- Dashboard coverage does not match alert coverage for stream backlog, inventory conflicts, worker totals, and recon gateway latency.
- Kafka readiness checks brokers serially under one shared 1s budget, which can false-negative multi-broker deployments.
- Monitoring docs send operators to `http://localhost:80/metrics`, but nginx intentionally returns 403 for `/metrics`.

**Nice-to-have**:
- Monitoring docs still reference removed `observability/metrics.go` rather than the split `metrics_*.go` files.

**Grade**: B-.

### 5. Security posture

**Critical**: None found in reachable code. `govulncheck` reports 0 reachable vulnerabilities.

**Important**:
- Auth is intentionally missing on customer and mutation surfaces. `GET /orders/:id` is readable by anyone with an order id, and `POST /events` is unauthenticated. This is acceptable for a simulator only if framed as pre-auth.
- The pprof/admin-loglevel listener is gated by `ENABLE_PPROF=false` and loopback default, but compose still publishes `6060:6060`. If `PPROF_ADDR` is widened, heap/profile/log-level control can become host-exposed.
- Idempotency keys are global Redis keys today, not user-scoped. This is okay pre-auth, but should be revisited with N9 auth/ownership.
- Redis GET/SET idempotency failures fail open for availability. That tradeoff is documented/tested, but it should be explicitly included in the threat model because retries during Redis instability can create duplicate in-flight work.

**Low**:
- Request body cap, trusted-proxy fatal config, nginx rate limit on `/api/`, and `/metrics` deny rules are solid security hardening already in place.

**Grade**: B.

### 6. Performance regressions

**Critical**:
- Current 60s / 500 VU benchmark runs exhaust the 500,000-ticket pool. Recent raw output shows only about 500k accepted bookings out of multi-million iterations, so headline RPS is heavily measuring the sold-out fast path rather than sustained accepted booking throughput.

**Important**:
- Async worker throughput remains under-measured. API benchmarks explicitly do not prove worker drain, DB transaction throughput, payment worker flow, or outbox creation capacity.
- Recon and saga watchdog sweeps are serial and backed by heuristic defaults; there is no backlog-clear benchmark or modeled SLO for these loops.

**Nice-to-have**:
- Header-bearing idempotency benchmarks are honest and useful, but they share the sold-out-heavy workload.
- pprof evidence is stale relative to Go 1.25.9 and the post-Phase-2 hot-path-adjacent changes.
- Current CP4c branch does not appear to require a new benchmark because it touches migration round-trip test/support, not the booking hot path.

**Grade**: B-.

### 7. Tech debt inventory

**Critical**: None. Production `TODO` / `FIXME` / `XXX` / `HACK` markers are effectively clean; the only meaningful TODO is an integration-test spec-gap note.

**Important**:
- Debt ledger is missing/stale. Several docs route deferred work to `architectural_backlog.md`, but that file is absent; the prior checkpoint still has `Outcome: TBD`.
- `GET /events/:id` remains a test-pinned stub and increments page-view metrics despite not loading event details.
- A4 transitional order-state edges (`Pending -> Confirmed/Failed`) remain legal without an enforced retirement path.
- `docs/reviews/SMOKE_TEST_PLAN.md` is stale enough to mislead reviewers: it expects old 200/`booking successful` behavior and says payment worker metrics do not exist.
- Mock gateway failure rate is hardcoded at 95%, so saga/E2E failure tests remain statistical rather than deterministic.

**Nice-to-have**:
- Product decision remains unresolved: should `compensated` allow re-buy?
- `Logger.S()` appears unused locally; keep or remove deliberately.
- Prometheus default-registry coupling is acknowledged but only tracked in the missing backlog.

**Debt score**: 68/100.

### 8. Senior-grade defensibility

- The “100k+ concurrent users” claim is the most challengeable. Current standard benchmark is 500 VUs, and older 5,000-VU evidence was weak. Safer framing: single-node high-throughput flash-sale simulator; horizontal-scale proof planned.
- README’s performance headline is stale relative to recent no-header and header-bearing benchmarks.
- Auth/user-ownership is explicitly missing; present as pre-auth unless N9 lands first.
- Backpressure is alert/runbook-based, not enforced at the producer.
- Payment remains a simulator, not real checkout semantics with provider intent/charge ids.
- DLQ is producer-complete but recovery-incomplete; phrase as operator-visible quarantine, not self-healing recovery.
- Idempotency is strong, but scope is currently `/book`, global keys, and fail-open cache behavior.
- `GET /events/:id` is demo-visible and should be fixed before external presentation.

**Grade**: A- as a high-throughput systems portfolio project; B if pitched as production-ready.

## Action plan

| # | Finding | Severity | Estimated effort | Target PR |
| :-- | :-- | :-- | :-- | :-- |
| 1 | Fix alert delivery defaults or document/install a real local notification receiver; avoid `receiver: null` for critical routes. | Critical | M | CP5 cleanup |
| 2 | Correct failing runbook SQL for recon/saga max-age incidents. | Critical | S | CP5 cleanup |
| 3 | Restore agent-doc bilingual contract: move/copy active AGENTS pair or update contract + hooks to root/current paths. | Critical | M | Docs cleanup |
| 4 | Refresh database/schema/migration docs in `PROJECT_SPEC` and root `AGENTS`. | Critical | M | Docs cleanup |
| 5 | Fix benchmark ticket-pool exhaustion so standard runs measure accepted booking throughput. | Critical | M | Perf cleanup |
| 6 | Implement real `GET /events/:id` or downgrade docs/tests to say it is a stub. | Important | S/M | API cleanup |
| 7 | Add `TargetDown` scrape-failure alert and align dashboard panels with alert inventory. | Important | M | Ops cleanup |
| 8 | Create or retire `architectural_backlog.md`; update checkpoint outcomes. | Important | S | Docs/debt cleanup |
| 9 | Add deterministic payment failure-rate config for mock gateway tests. | Important | S | Test cleanup |
| 10 | Define SLO/error-budget targets and decide whether to implement burn-rate alerts now or defer explicitly. | Important | M | Ops roadmap |

## Defensibility self-critique summary

The strongest story is the reliability arc: UUIDv7 order identity through the async path, explicit state transitions with audit history, transactional outbox, saga compensation, idempotency fingerprinting, and recent integration coverage. The weakest external-facing claims are production readiness, 100k concurrency, and headline RPS unless the benchmark pool is fixed. The project reads as senior and thoughtful when framed honestly as a high-throughput simulator with a visible hardening roadmap; it reads over-claimed if presented as a fully production-ready ticketing platform.

## Outcome

**Cleanup split landed in two PRs.**

- **Ops/perf cleanup** (this PR / branch `chore/codex-review-followup-ops-perf`, 2026-05-02) — addresses Critical #1 (alert delivery: webhook-logger sidecar replaces null receiver), #2 (runbook SQL column names + the `SagaMaxFailedAgeExceeded` query semantic was further corrected to target `orders` not `order_status_history`), #5 (benchmark methodology — added `accepted_bookings` k6 counter so reports separate total RPS from accepted-booking RPS; ticket pool kept at 500k as the realistic flash-sale shape rather than inflated to a non-scarcity number); plus Important #7 (TargetDown scrape-failure alert + dashboard panel + runbook with planned-maintenance silence guidance).

- **Docs/API cleanup** (deferred next PR) — Critical #3 (AGENTS bilingual contract restoration), Critical #4 (PROJECT_SPEC schema doc refresh), and the `GET /events/:id` stub-or-implement decision. The Codex-produced AGENTS / `.agents/` / `.codex/` artifacts are stashed off-branch waiting on that PR.

**Pre-existing operational surprise discovered during smoke testing.** When the new webhook-logger receiver came online, `OrdersDLQNonEmpty` and `OrdersStreamBacklogRed` immediately delivered to the sidecar with `OrdersDLQNonEmpty.description = "12092 entries in DLQ for 5m+"`. These alerts had been firing in Alertmanager for some time but were silently swallowed by the prior null receiver — exactly the operability gap Critical #1 was about. **Triage required separately** (out of scope for this PR): is the DLQ accumulation a stress-test artifact, a stuck consumer, or a payment-worker failure mode? Drain or investigate before treating the cleanup as complete.

**Defensibility-grade unchanged by this PR.** The review's grade (A− as portfolio simulator / B if pitched production-ready) reflected over-claimed performance + auth gaps; those are addressed in different roadmap phases (Phase 3 Pattern A demo + N9 auth/RBAC).
