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
POST /book                 # Legacy route (Phase 0 retained)
```

## Database
- PostgreSQL on port 5433 (user/password/booking)
- 3 tables: `events`, `orders`, `events_outbox`
- 6 migrations in `deploy/postgres/migrations/`

## Kafka Topics
- `order.created` - consumed by payment service
- `order.failed` - consumed by saga compensator

## Development Conventions
- Immutable data patterns - create new objects, never mutate
- Files under 800 lines, functions under 50 lines
- Handle errors explicitly at every level
- Validate at system boundaries
- No hardcoded secrets - use env vars
- Tests use testify/assert + go.uber.org/mock

## Current State (as of 2026-02-24)
12 phases completed. Core system fully functional with dual-tier inventory, async processing, outbox, payment flow, and saga compensation. See [../docs/PROJECT_SPEC.md](../docs/PROJECT_SPEC.md) for full details.

## Remaining Roadmap
- DLQ Worker (dead letter retry policy)
- Event Sourcing / CQRS
- Horizontal scaling tests
- Real payment gateway integration

## Available Tooling under `.claude/`

Claude Code auto-discovers assets placed under `.claude/agents/` and `.claude/skills/`. These are adopted from [affaan-m/everything-claude-code](https://github.com/affaan-m/everything-claude-code) (MIT) — see [.claude/ATTRIBUTIONS.md](ATTRIBUTIONS.md).

### Subagents (`.claude/agents/`)
- **go-reviewer** — Use on any Go code change. Checks security (SQL/command injection, race conditions, `InsecureSkipVerify`), error handling (wrapping, `errors.Is/As`), concurrency (goroutine leaks, channel deadlocks), and code quality.
- **go-build-resolver** — Use when `go build` or `go test` fails. Diagnoses import cycles, version mismatches, module errors.
- **silent-failure-hunter** — Use when reviewing error-handling paths, especially in Kafka consumers ([internal/infrastructure/messaging/](../internal/infrastructure/messaging/)), the outbox relay ([internal/application/outbox_relay.go](../internal/application/outbox_relay.go)), saga compensator, and worker service. Hunts swallowed errors, empty catch blocks, and bad fallbacks.

### Skills (`.claude/skills/`)
- **golang-patterns** — Go idioms: small interfaces, error wrapping, context propagation.
- **golang-testing** — Table-driven tests, `testify` / `go.uber.org/mock`, race detection, coverage.
- **postgres-patterns** — PG-specific: transactions, advisory locks, indexes, connection pooling.
- **tdd-workflow** — Red-green-refactor loop, operationalizing the TDD mandate in the global coding style.

### Rules (`.claude/rules/golang/`)
Extends the user's global `~/.claude/rules/common/` with Go-specific standards: `coding-style.md`, `hooks.md`, `patterns.md`, `security.md`, `testing.md`.
