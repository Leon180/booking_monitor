# Booking Monitor - Claude Code Instructions

> 中文版本: [CLAUDE.zh-TW.md](CLAUDE.zh-TW.md)

## ⚠️ Bilingual Documentation Contract (MANDATORY)

This project maintains **paired English + Traditional Chinese (zh-TW)** versions of three documents. These files are considered a **single logical unit**. When editing ANY of them, you MUST update both language versions in the **same response**, keeping them structurally identical (same sections, same tables, same ordering).

**Paired files (relative to repo root):**
| English | Chinese |
|---------|---------|
| `.claude/CLAUDE.md` | `.claude/CLAUDE.zh-TW.md` |
| `README.md` | `README.zh-TW.md` |
| `docs/PROJECT_SPEC.md` | `docs/PROJECT_SPEC.zh-TW.md` |
| `docs/monitoring.md` | `docs/monitoring.zh-TW.md` |

**Rules:**
1. **Never** edit only one side of a pair. If you cannot translate, ask the user instead of skipping.
2. **Structural parity**: section headings, table rows, and ordering must match 1:1 between the two versions.
3. **Code/commands/filenames stay in English** in both versions — only prose is translated.
4. **When adding a new section**, add it to both versions in the same response, in the same place.
5. **When the user asks to update one of these files**, acknowledge that the paired file will also be updated, then do both edits before marking the task complete.
6. **Translation style**: zh-TW should use 台灣繁體中文 conventions (e.g. 資料庫 not 数据库, 介面 not 接口, 物件 not 对象).

**Note**: Claude Code auto-loads both `./CLAUDE.md` and `./.claude/CLAUDE.md` with equal priority, so this file (at `.claude/CLAUDE.md`) is loaded directly — no root stub needed. Only the English version is auto-loaded; `.claude/CLAUDE.zh-TW.md` is human-readable reference. A PostToolUse hook (`.claude/hooks/check_bilingual_docs.sh`) enforces the contract at edit time.

## ⚠️ Monitoring Docs Contract

The day-to-day operator's monitoring guide lives at [docs/monitoring.md](../docs/monitoring.md) (+ paired zh-TW). It documents the metric inventory, Prometheus / Grafana workflow, alert catalog, and recipes to force alerts to fire for testing.

A second PostToolUse hook (`.claude/hooks/check_monitoring_docs.sh`) fires whenever any observability surface is touched — `internal/infrastructure/observability/metrics.go`, any `*_collector.go`, `deploy/prometheus/alerts.yml`, `deploy/prometheus/prometheus.yml`, or `deploy/grafana/provisioning/dashboards/*.json` — and reminds Claude to update the guide before ending the turn. Add a new metric without updating §2; add a new alert without updating §5; the hook will catch it.

---

## Project Overview
High-concurrency ticket booking system (flash sale simulator) built with Go. Uses DDD + Clean Architecture in a modular monolith. Dual-tier inventory (Redis hot path + PostgreSQL source of truth) with async processing via Redis Streams, event publishing via Kafka outbox pattern, and saga-based payment compensation.

## Tech Stack
Go 1.25 | Gin | PostgreSQL 15 | Redis 7 | Kafka | Prometheus | Grafana | Jaeger | Nginx

## Architecture Layers
```
internal/
  domain/         # Entities (Event, Order), interfaces (repos, services)
  application/    # Services: BookingService, WorkerService, OutboxRelay, SagaCompensator, PaymentService
  infrastructure/ # Adapters: api/, cache/, persistence/postgres/, messaging/, observability/, payment/, config/
  mocks/          # Generated mocks (go.uber.org/mock)
```

## Key Commands
```bash
make build          # Build binary with -race
make test           # Run tests with race detection
make run-server     # Start API server (port 8080)
make run-stress     # Load test (C=concurrency, N=requests)
make stress-k6      # K6 load test (VUS=500, DURATION=30s)
make reset-db       # Clear orders, reset inventory to 100
make migrate-up     # Run database migrations
make mocks          # Generate mock files
docker-compose up -d  # Full stack (app, nginx, payment_worker, postgres, redis, kafka, prometheus, grafana, jaeger)
```

## Key Patterns
- **Transactional Outbox**: Order + OutboxEvent in same DB transaction -> OutboxRelay polls -> Kafka publish
- **Saga Compensation**: Payment fails -> order.failed topic -> SagaCompensator rolls back DB + Redis inventory
- **Unit of Work**: Context-injected transactions via `PostgresUnitOfWork`
- **Advisory Locks**: PostgreSQL `pg_try_advisory_lock(1001)` for OutboxRelay leader election
- **Redis Lua Scripts**: `deduct.lua` (atomic inventory decrement + stream publish), `revert.lua` (idempotent compensation)
- **Idempotency**: API (Idempotency-Key header), worker (DB unique constraint), saga (Redis SETNX)

## API Endpoints
```
POST /api/v1/book          # Book tickets (user_id, event_id, quantity) → 202 with order_id + poll URL
GET  /api/v1/orders/:id    # Poll order status by id (returns 404 during the brief async-processing window)
GET  /api/v1/history       # Paginated order history (?page=&size=&status=)
POST /api/v1/events        # Create event (name, total_tickets)
GET  /api/v1/events/:id    # View event details
GET  /metrics              # Prometheus metrics
GET  /livez                # Liveness probe — always 200 if process is up
GET  /readyz               # Readiness probe — 200 only if PG + Redis + Kafka all answer within 1s; 503 with per-dep JSON otherwise
```

Legacy `POST /book` was removed in Phase 13 remediation (PR #9 H9) — it bypassed nginx rate-limit zones. All callers must use `/api/v1/book`.

**Booking response contract (PR #47).** `POST /api/v1/book` returns 202 Accepted with `{order_id, status: "processing", message, links: {self}}`. The 202 is honest about the async pipeline: Redis-side inventory deduct succeeded (the load-shed gate); DB persistence + payment + saga are in flight. Clients poll `GET /api/v1/orders/:id` for the terminal status — that endpoint may return 404 for ~ms after 202 (async-processing window) and clients should retry with backoff. The `order_id` is a UUIDv7 minted at the API boundary in `BookingService.BookTicket` and threaded through Redis stream → worker → DB → outbox → saga; PEL retries reuse the same id rather than minting a fresh one per delivery.

`/livez` + `/readyz` follow k8s probe conventions: liveness must NOT depend on downstream services (a Redis blip cannot be allowed to kill every pod), readiness pings real dependencies. The compose `app` service uses `/livez` as its HEALTHCHECK.

## Database
- PostgreSQL on port 5433 (user/password/booking)
- 3 tables: `events`, `orders`, `events_outbox`
- 7 migrations in `deploy/postgres/migrations/` (000007 added in PR #12: partial index on `events_outbox(id) WHERE processed_at IS NULL`)

## Kafka Topics
- `order.created` — consumed by payment service (group `payment-service-group`)
- `order.created.dlq` — dead letter for unparseable / invalid payment events
- `order.failed` — consumed by saga compensator (group `booking-saga-group`)
- `order.failed.dlq` — dead letter for saga events that exceed `sagaMaxRetries=3`

Group IDs and topic names are configurable via `KAFKA_PAYMENT_GROUP_ID`, `KAFKA_ORDER_CREATED_TOPIC`, `KAFKA_SAGA_GROUP_ID`, `KAFKA_ORDER_FAILED_TOPIC`.

## CI

GitHub Actions at [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) runs on every push to `main` and every PR. Four jobs in parallel:

| Job | What | Why |
| :-- | :-- | :-- |
| `test (race)` | `go vet` + `go test -race -coverprofile ./internal/...` | Race detector loves CI — non-determinism only surfaces in volume. Coverage uploaded as artifact (no gate). |
| `lint (golangci-lint)` | `golangci-lint run` against [`.golangci.yml`](../.golangci.yml) | Conservative set: errcheck, govet, ineffassign, staticcheck, gosec, revive. Style linters (gocyclo, funlen, lll) deliberately deferred until correctness baseline is clean. |
| `govulncheck (supply chain)` | `govulncheck ./...` | Maps known CVEs to actual call paths — only fails when a vulnerable symbol is reachable from our code, not on every transitive import. |
| `docker build` | Multi-stage Dockerfile build (no push) | Catches image-stage breakage that `make build` doesn't. |

Toolchain is pinned via `go.mod`'s `toolchain go1.25.9` directive so CI builds (and any developer using `go install` against this module) automatically get stdlib CVE patches. Bump in lockstep with `Dockerfile`'s `golang:1.25-alpine` tag.

## Development Conventions
- Immutable data patterns - create new objects, never mutate
- Files under 800 lines; functions under 50 lines by default. Bootstrap / DI wiring / linear-construction code (e.g. `cmd/booking-cli/main.go` fx.Invoke bodies) may go up to ~80 lines when splitting would only add indirection without clarifying intent. Do not extract helpers purely to meet the line count.
- Handle errors explicitly at every level
- Validate at system boundaries
- No hardcoded secrets - use env vars
- Tests use testify/assert + go.uber.org/mock

## Benchmark Conventions

Throughput regressions are tracked under `docs/benchmarks/`. The directory layout and contents are convention, not tool-enforced — keep it consistent so historical runs remain comparable.

**Directory naming**: `YYYYMMDD_HHMMSS_compare_c<vus>[_<tag>]` (e.g. `20260426_183530_compare_c500_pr35`). The `c<vus>` segment is the VU count; the optional trailing `_<tag>` describes what is being compared (a PR id, a phase name, etc.).

**Required artifacts** per directory:
- `comparison.md` — parameters, run-A vs run-B metric table, conclusion, caveats (see `20260426_183530_compare_c500_pr35/comparison.md` as the canonical template)
- `run_a_raw.txt` — full k6 stdout of the baseline run
- `run_b_raw.txt` — full k6 stdout of the under-test run

**Standard apples-to-apples conditions** (use these unless deliberately measuring something else):
- Script: `scripts/k6_comparison.js`
- VUs: 500
- Duration: 60s
- Ticket pool: 500,000 (so the run does not hit sold-out — measures pure capacity)
- Target: `http://app:8080/api/v1` (direct, bypasses nginx rate limit)
- Both runs against an equivalent Docker stack on the same host

**When to record**: PRs that touch the booking hot path (handler / `BookingService.BookTicket` / Redis Lua / `OrderMessageProcessor.Process` tx body / outbox relay polling) MUST land a comparison report. Pure refactors that the diff demonstrably leaves the hot path byte-identical (see PR 31→35 for the case that prompted this rule) MAY skip the report — but a report verifying "no regression" is still the cleanest evidence and is preferred.

**Existing tooling**: `make benchmark-compare VUS=500 DURATION=60s` runs `scripts/benchmark_compare.sh` which produces the directory + raw outputs automatically; `comparison.md` is then hand-written referencing the captured raw files. Run-to-run variance for k6-on-Docker laptop is typically 3-5%; deltas below that are noise, not signal.

## Current State (as of 2026-04-24)
15 phases completed. Phase 13 (Apr 11) landed 66 remediation findings across PRs #7/#8/#9/#12/#13. Phase 14 (Apr 12–13) landed GC optimization in PRs #14/#15 — pprof harness, sampler tuning, `GOGC=400`, `GOMEMLIMIT=256MiB`, sync.Pool for Redis Lua args, combined middleware (1 `context.WithValue` + 1 `c.Request.WithContext` per request). Phase 15 (Apr 23–24) landed the logger architecture refactor in PR #18 — `pkg/logger/` → `internal/log/` with ctx-aware emit methods (`Debug/Info/Warn/Error/Fatal(ctx, msg, fields...)`) that auto-inject `correlation_id` + OTEL `trace_id`/`span_id`, `/admin/loglevel` runtime level endpoint on the pprof listener, and the typed `internal/log/tag/` field constructors. Post-Phase-15 (Apr 24) review cleanup in PRs #21/#22/#23: strict review of `cmd/booking-cli/main.go` landed the P0 stress URL fix, payment-worker tracer init, pprof loopback binding + timeouts, `fx.Shutdowner` escalation for failing goroutines, function decomposition, and promotion of pprof + trusted-proxy + db-ping tunables into `config.Config` (with `KAFKA_BROKERS` + `TRUSTED_PROXIES` switching to cleanenv `[]string` + `env-separator`, eliminating a silent multi-broker bug). See [../docs/PROJECT_SPEC.md](../docs/PROJECT_SPEC.md) for full details.

## Logging Conventions (post-PR #18)
- **Pattern A — long-lived components**: inject `*log.Logger` via constructor and decorate with `component=<subsystem>` via `With()` ONCE at construction (e.g., `worker_service`, `outbox_relay`, `saga_compensator`). Use `l.Error(ctx, "msg", tag.OrderID(id))` — ctx-aware methods enrich with correlation/trace ids automatically.
- **Pattern B — call-site-local code**: handlers, middleware, init paths use package-level `log.Error(ctx, "msg", tag.UserID(uid))`. Reads the logger from ctx via `FromContext`, falls back to Nop when unset.
- **Typed fields**: prefer `tag.OrderID`/`tag.Error`/`tag.UserID` etc. from `internal/log/tag/` over raw `log.Int("order_id", ...)` — compile-time typo protection.
- **Inline one-off fields**: use `log.String`/`log.Int`/`log.Int64`/`log.ByteString`/`log.Err`/`log.NamedError` from `internal/log/` for keys that don't warrant a typed tag (`component`, `batch_size`, `payload`, etc.). Do NOT import `go.uber.org/zap` from application code — zap is encapsulated inside `internal/log/`.
- **Never call `zap.S()` or `zap.L()` globals** — they're not wired in this codebase; the logger is DI'd everywhere.

## Remaining Roadmap
- DLQ Worker (dead letter retry policy)
- Event Sourcing / CQRS
- Horizontal scaling tests
- Real payment gateway integration

## Key Env Vars
See [docs/PROJECT_SPEC.md § 7](../docs/PROJECT_SPEC.md) for the full list. Most-used knobs:

**Runtime / GC / Tracing**
- `GOGC` (default `400` in `.env`, `100` fallback in docker-compose) — higher = less frequent GC
- `GOMEMLIMIT=256MiB` — soft memory limit; pairs with GOGC so GC gets aggressive only near the cap
- `OTEL_TRACES_SAMPLER_RATIO` (default `0.01`) — 1% sampling; `1` = always, `0` = never

**Security-sensitive (post-PR #21/#22)**
- `ENABLE_PPROF` (default `false`) — when `true`, starts the pprof + `/admin/loglevel` listener
- `PPROF_ADDR` (default `127.0.0.1:6060`) — **loopback by default**; override only when remote pprof is genuinely needed. Heap dumps + log-level control live here
- `TRUSTED_PROXIES` (CSV, default RFC1918 CIDRs) — Gin trusts these for `ClientIP()`; override for service meshes outside RFC1918 (GKE, some EKS)

**Operational (post-PR #21/#22)**
- `CONFIG_PATH` (default `config/config.yml`) — config path; needed when CWD differs (systemd units, k8s initContainers)
- `DB_PING_ATTEMPTS` / `DB_PING_INTERVAL` / `DB_PING_PER_ATTEMPT` — DB startup probe budget; raise attempts for slow dependencies
- `KAFKA_BROKERS` (CSV, default `localhost:9092`) — now parsed as `[]string` via cleanenv's `env-separator:","`

**Worker / Cache (post-PR #37)**
- `WORKER_STREAM_READ_COUNT` (default `10`) / `WORKER_STREAM_BLOCK_TIMEOUT` (default `2s`) — XReadGroup batch + block window for the order-stream consumer
- `WORKER_MAX_RETRIES` (default `3`) / `WORKER_RETRY_BASE_DELAY` (default `100ms`) — per-message retry budget + linear-backoff base; deterministic-failure errors bypass via the application-level retry policy
- `WORKER_FAILURE_TIMEOUT` (default `5s`) — handleFailure compensation ctx budget (Redis revert + DLQ XAdd)
- `WORKER_PENDING_BLOCK_TIMEOUT` (default `100ms`) / `WORKER_READ_ERROR_BACKOFF` (default `1s`) — startup PEL sweep block + read-error retry sleep
- `REDIS_INVENTORY_TTL` (default `720h`) / `REDIS_IDEMPOTENCY_TTL` (default `24h`) — Redis cache key lifetimes; previously hardcoded as const
- `REDIS_MAX_CONSECUTIVE_READ_ERRORS` (default `30`) — broken-Redis tolerance before the worker exits

## Available Tooling under `.claude/`

Claude Code auto-discovers assets placed under `.claude/agents/` and `.claude/skills/`. These are adopted from [affaan-m/everything-claude-code](https://github.com/affaan-m/everything-claude-code) (MIT) — see [.claude/ATTRIBUTIONS.md](ATTRIBUTIONS.md).

### Subagents (`.claude/agents/`)
- **go-reviewer** — TRIGGER: any `*.go` file modified in a PR. Checks security (SQL/command injection, race conditions, `InsecureSkipVerify`), error handling (wrapping, `errors.Is/As`), concurrency (goroutine leaks, channel deadlocks), and code quality.
- **go-build-resolver** — TRIGGER: `go build` or `go test` fails. Diagnoses import cycles, version mismatches, module errors.
- **silent-failure-hunter** — TRIGGER: reviewing code that returns or swallows errors, especially Kafka consumers ([internal/infrastructure/messaging/](../internal/infrastructure/messaging/)), the outbox relay ([internal/application/outbox_relay.go](../internal/application/outbox_relay.go)), saga compensator, and worker service. Hunts swallowed errors, empty catch blocks, and bad fallbacks.

### Skills (`.claude/skills/`)
- **golang-patterns** — TRIGGER: writing new Go code. Go idioms: small interfaces, error wrapping, context propagation.
- **golang-testing** — TRIGGER: adding tests. Table-driven tests, `testify` / `go.uber.org/mock`, race detection, coverage.
- **postgres-patterns** — TRIGGER: touching `internal/infrastructure/persistence/postgres/` or migrations. Transactions, advisory locks, indexes, connection pooling.
- **tdd-workflow** — TRIGGER: starting a new feature/bugfix. Red-green-refactor loop, operationalizing the TDD mandate in the global coding style.

### Rules (`.claude/rules/golang/`)
Extends the user's global `~/.claude/rules/common/` with Go-specific standards: `coding-style.md`, `hooks.md`, `patterns.md`, `security.md`, `testing.md`.
