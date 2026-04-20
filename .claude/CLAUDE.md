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

**Rules:**
1. **Never** edit only one side of a pair. If you cannot translate, ask the user instead of skipping.
2. **Structural parity**: section headings, table rows, and ordering must match 1:1 between the two versions.
3. **Code/commands/filenames stay in English** in both versions — only prose is translated.
4. **When adding a new section**, add it to both versions in the same response, in the same place.
5. **When the user asks to update one of these files**, acknowledge that the paired file will also be updated, then do both edits before marking the task complete.
6. **Translation style**: zh-TW should use 台灣繁體中文 conventions (e.g. 資料庫 not 数据库, 介面 not 接口, 物件 not 对象).

**Note**: Claude Code auto-loads both `./CLAUDE.md` and `./.claude/CLAUDE.md` with equal priority, so this file (at `.claude/CLAUDE.md`) is loaded directly — no root stub needed. Only the English version is auto-loaded; `.claude/CLAUDE.zh-TW.md` is human-readable reference. A PostToolUse hook (`.claude/hooks/check_bilingual_docs.sh`) enforces the contract at edit time.

---

## Project Overview
High-concurrency ticket booking system (flash sale simulator) built with Go. Uses DDD + Clean Architecture in a modular monolith. Dual-tier inventory (Redis hot path + PostgreSQL source of truth) with async processing via Redis Streams, event publishing via Kafka outbox pattern, and saga-based payment compensation.

## Tech Stack
Go 1.24 | Gin | PostgreSQL 15 | Redis 7 | Kafka | Prometheus | Grafana | Jaeger | Nginx

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
POST /api/v1/book          # Book tickets (user_id, event_id, quantity)
GET  /api/v1/history       # Paginated order history (?page=&size=&status=)
POST /api/v1/events        # Create event (name, total_tickets)
GET  /api/v1/events/:id    # View event details
GET  /metrics              # Prometheus metrics
```

Legacy `POST /book` was removed in Phase 13 remediation (PR #9 H9) — it bypassed nginx rate-limit zones. All callers must use `/api/v1/book`.

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

## Development Conventions
- Immutable data patterns - create new objects, never mutate
- Files under 800 lines, functions under 50 lines
- Handle errors explicitly at every level
- Validate at system boundaries
- No hardcoded secrets - use env vars
- Tests use testify/assert + go.uber.org/mock

## Current State (as of 2026-04-21)
14 phases completed. Phase 13 (Apr 11) landed 66 remediation findings across PRs #7/#8/#9/#12/#13. Phase 14 (Apr 12–13) landed GC optimization in PRs #14/#15 — pprof harness, sampler tuning, `GOGC=400`, `GOMEMLIMIT=256MiB`, sync.Pool for Redis Lua args, combined middleware (1 `context.WithValue` + 1 `c.Request.WithContext` per request). See [../docs/PROJECT_SPEC.md](../docs/PROJECT_SPEC.md) for full details.

## Remaining Roadmap
- DLQ Worker (dead letter retry policy)
- Event Sourcing / CQRS
- Horizontal scaling tests
- Real payment gateway integration

## Key Env Vars (GC / Tracing / Profiling)
- `GOGC` (default `400` in `.env`, `100` fallback in docker-compose) — higher = less frequent GC
- `GOMEMLIMIT=256MiB` — soft memory limit; pairs with GOGC so GC gets aggressive only near the cap
- `OTEL_TRACES_SAMPLER_RATIO` (default `0.01`) — 1% sampling; `1` = always, `0` = never
- `ENABLE_PPROF` (default `false`) — when `true`, pprof endpoints served on `:6060`

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
