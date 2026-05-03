# Booking Monitor System

> 中文版本: [README.zh-TW.md](README.zh-TW.md)

A high-concurrency ticket booking system simulation designed for flash sale scenarios (100k+ concurrent users). Evolves from direct DB usage to advanced caching, queueing, and saga patterns.

## Architecture

Current production shape (Stage 4 — see [Architecture Evolution](#architecture-evolution) below for how we got here):

```mermaid
flowchart LR
    Client((Client)) --> Nginx[Nginx<br/>rate limit]
    Nginx --> API[Gin API]
    API -->|Lua DECRBY + XADD| Redis[(Redis<br/>inventory + stream)]
    Redis -.->|XReadGroup| Worker
    Worker -->|UoW: INSERT order<br/>+ events_outbox| PG[(Postgres<br/>source of truth)]
    PG -.->|advisory-lock<br/>leadered relay| Kafka{{Kafka}}
    Kafka -.->|order.created| Payment[Payment Worker]
    Payment -.->|fail| Kafka
    Kafka -.->|order.failed| Saga[Saga<br/>Compensator]
    Saga --> Redis
    Saga --> PG
```

> Solid arrows = synchronous hot path · dashed arrows = async / event-driven
> See [v0.4.0 release notes](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0) for the cache-truth contract: Redis ephemeral, Postgres source-of-truth, drift detected and named.

**Design**: Domain-Driven Design + Clean Architecture (Modular Monolith)

```
cmd/booking-cli/          # CLI entry: server, stress, payment, recon, saga-watchdog subcommands
internal/
  domain/                 # Entities (Event, Order, OutboxEvent), value types (StuckCharging, StuckFailed), repository interfaces
  application/            # Cross-package fx module + UnitOfWork interface + wire-format event DTOs (order_events.go)
    booking/              # POST /book hot path: BookTicket validates + Redis Lua deduct
    worker/               # Order-stream consumer + queue policy + per-message processor
    outbox/               # Outbox relay polling + Kafka publish (transactional outbox impl)
    event/                # Event creation + Redis hot inventory provisioning
    payment/              # Kafka order.created consumer + gateway orchestration + saga trigger
    recon/                # Reconciler (A4) — sweeps stuck `charging` orders, queries gateway, resolves
    saga/                 # Compensator + Watchdog (A5) — order.failed consumer + DB-side sweep
  infrastructure/
    api/
      booking/            # POST /book, GET /orders, GET /history, POST /events, GET /events/:id
      ops/                # /livez, /readyz, /metrics
      middleware/         # Idempotency (N4), correlation_id, metrics
      dto/                # Wire-format request/response shapes
    cache/                # Redis: inventory, streams, idempotency, Lua scripts (deduct.lua, revert.lua)
    persistence/postgres/ # Repositories, UoW, advisory locks, row mappers
    messaging/            # Kafka publisher + consumers
    observability/        # Prometheus metrics, OTEL tracing, DB-pool collector
    payment/              # Mock payment gateway with configurable success rate
    config/               # YAML config + env overrides (cleanenv)
  log/                    # Structured logging (Zap) — context propagation, typed tags, runtime level
  bootstrap/              # fx wiring for logger + tracer + DI primitives
deploy/                   # Postgres migrations (11), Redis Lua, Nginx, Prometheus alerts, Grafana dashboards
```

### Architecture Evolution

The current Stage 4 didn't appear all at once. Each layer was added in response to a benchmark-documented bottleneck — the four-stage progression below is the architecture-evolution story you can walk through commit-by-commit on the [Releases page](https://github.com/Leon180/booking_monitor/releases).

**Stage 1 — synchronous baseline.** API → Postgres `SELECT FOR UPDATE`. Saturates well below 1k req/s due to row-lock contention. The C500 benchmark on [v0.1.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.1.0) documented this ceiling.

```mermaid
flowchart LR
    C1((Client)) --> A1[API]
    A1 -->|SELECT FOR UPDATE<br/>+ INSERT| P1[(Postgres)]
```

**Stage 2 — Redis hot path, sync DB write.** Lua atomic deduct on Redis removes the row-lock contention, but the synchronous DB write becomes the new bottleneck.

```mermaid
flowchart LR
    C2((Client)) --> A2[API]
    A2 -->|Lua DECRBY| R2[(Redis)]
    A2 -->|sync INSERT| P2[(Postgres)]
```

**Stage 3 — async via Redis Streams + worker pool.** Lua deduct emits to a stream; a worker pool drains it asynchronously. Customer gets HTTP 202 the moment Redis confirms.

```mermaid
flowchart LR
    C3((Client)) --> A3[API]
    A3 -->|Lua DECRBY + XADD| R3[(Redis)]
    R3 -.->|stream| W3[Worker]
    W3 -->|INSERT| P3[(Postgres)]
```

**Stage 4 — full event-driven (current).** Worker writes order + outbox in one UoW; advisory-lock-leadered relay pushes to Kafka; payment service + saga compensator are independent consumers. This is the architecture released as [v0.2.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.2.0) and hardened through [v0.3.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.3.0) + [v0.4.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0).

```mermaid
flowchart LR
    C4((Client)) --> A4[API]
    A4 -->|Lua DECRBY + XADD| R4[(Redis)]
    R4 -.->|stream| W4[Worker]
    W4 -->|UoW: order + outbox| P4[(Postgres)]
    P4 -.->|relay| K4{{Kafka}}
    K4 -.-> Pay4[Payment]
    K4 -.-> S4[Saga]
```

The 4-stage `cmd/booking-cli-stage{1,2,3,4}/` comparison harness (D12 in [`docs/post_phase2_roadmap.md`](docs/post_phase2_roadmap.md)) is planned for Phase 3 — same `internal/` packages, different fx wirings, side-by-side benchmark runs.

### Pattern A flow (planned — Phase 3a)

The next milestone splits `POST /book` into reservation + explicit `POST /pay` + `POST /webhook/payment`. Brings the booking flow into Stripe Checkout / KKTIX shape.

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant API
    participant R as Redis
    participant W as Worker
    participant P as Postgres
    participant GW as Payment<br/>Gateway
    participant SW as Expiry<br/>Sweeper

    rect rgb(240, 248, 255)
    Note over C,P: Reservation (D1-D3)
    C->>+API: POST /book {event_id, qty}
    API->>+R: Lua DECRBY + XADD
    R-->>-API: ok
    API-->>-C: 202 {order_id, awaiting_payment,<br/>reserved_until, payment_intent}
    R-->>W: XReadGroup
    W->>P: INSERT order status=awaiting_payment
    end

    alt Customer pays before TTL (D4-D5)
        rect rgb(245, 255, 245)
        C->>+API: POST /orders/:id/pay
        API->>+GW: CreatePaymentIntent
        GW-->>-API: client_secret
        API-->>-C: client_secret
        Note over C,GW: customer completes Stripe Elements
        GW->>+API: POST /webhook/payment (signed)
        API->>API: verify signature + idempotency
        API->>P: status awaiting_payment → paid
        API-->>-GW: 200
        end
    else TTL expires without payment (D6)
        rect rgb(255, 245, 245)
        SW->>P: SELECT awaiting_payment<br/>WHERE reserved_until < NOW()
        P-->>SW: stuck rows
        SW->>R: revert.lua INCRBY
        SW->>P: status awaiting_payment → expired
        end
    end
```

`v0.5.0` will be tagged when this flow is complete.

## Features

- **Dual-Tier Inventory**: Redis (hot path, sub-ms) + PostgreSQL (source of truth)
- **Async Processing**: Redis Streams with consumer groups and PEL recovery
- **Transactional Outbox**: Atomic order + event persistence, Kafka publishing
- **Saga Compensation**: Idempotent payment failure rollback (DB + Redis)
- **Idempotency**: 4 levels — API (`Idempotency-Key` header + N4 fingerprint validation), worker (DB UNIQUE constraint), saga (Redis SETNX), payment gateway (mock implements idempotent `Charge`)
- **Rate Limiting**: Nginx (100 req/s/IP, burst 200)
- **Leader Election**: PostgreSQL advisory locks for single OutboxRelay instance
- **Full Observability**: Prometheus metrics, Grafana dashboards, Jaeger tracing, Zap logging
- **Correlation IDs**: End-to-end request tracking across all components

## Prerequisites

- Go 1.25+ (toolchain pinned via `go.mod` `toolchain go1.25.9`)
- Docker & Docker Compose
- `golangci-lint` (for linting)
- `golang-migrate` (for database migrations)
- K6 (optional, for load testing)

## Quick Start

1. **Start Infrastructure**:
   ```bash
   docker-compose up -d
   ```

2. **Run Migrations**:
   ```bash
   make migrate-up
   ```

3. **Build & Run Server**:
   ```bash
   make run-server
   ```
   Server listens on port 8080. Metrics at `/metrics`.

4. **Run Payment Worker** (separate terminal):
   ```bash
   ./bin/booking-cli payment
   ```

5. **Reset State** (for testing):
   ```bash
   make reset-db
   ```

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/book` | Submit a booking. Returns **202 Accepted** + an `order_id` to track it (see **Booking flow** below). |
| GET | `/api/v1/orders/:id` | Look up the latest status of one order by `order_id`. May return 404 in a brief window after `POST /book` (see **Booking flow**). |
| GET | `/api/v1/history` | Order history `?page=1&size=10&status=confirmed` |
| POST | `/api/v1/events` | Create event `{ name, total_tickets }` |
| GET | `/api/v1/events/:id` | **Stub** — returns `{"message": "View event", "event_id": ...}` + bumps `page_views_total` for conversion tracking. Does NOT load event details (deferred to Phase 3 demo). |
| GET | `/metrics` | Prometheus metrics |
| GET | `/livez` | Liveness probe — always 200 if process is up (no downstream deps) |
| GET | `/readyz` | Readiness probe — 200 only if PG + Redis + Kafka all answer within 1s; 503 + per-dep JSON otherwise |

### Booking flow

`POST /api/v1/book` is **asynchronous by design**. The 202 (not 200) is honest: at the moment of the response, only the Redis-side inventory deduct has happened — the order hasn't been written to DB, payment hasn't been attempted, the booking isn't actually confirmed yet. The client gets back an `order_id` and is expected to poll for the terminal status.

```
1. Client → POST /api/v1/book { user_id, event_id, quantity }
2. Server → 202 Accepted {
       order_id: "019dd493-47ae-79b1-b954-8e0f14a6a482",
       status:   "processing",
       message:  "booking accepted, awaiting confirmation",
       links:    { self: "/api/v1/orders/019dd493-..." }
   }

   At this point:
   - Redis inventory: deducted (the "load-shed gate")
   - DB orders row:   not yet (worker writes it ~ms later)
   - Payment:         not attempted yet
   - Outcome:         not yet known

3. Client → GET /api/v1/orders/<order_id>  (poll with backoff: 100ms → 250ms → 500ms ...)

   Possible responses:
   - 404  → worker hasn't persisted the row yet. Retry.
   - 200  → { id, user_id, event_id, quantity, status, created_at }
            where `status` is one of:
              "pending"     — DB persisted, awaiting payment
              "charging"    — payment in progress
              "confirmed"   — paid + booked       ✓ terminal (success)
              "failed"      — payment failed; saga will compensate
              "compensated" — saga has rolled back inventory  ✓ terminal (failure)
```

**Why async, not synchronous?** Redis-first acts as a **load-shed gate** — at flash-sale traffic, sold-out attempts get rejected at the Redis layer without ever touching DB. If `POST /book` blocked until terminal status, every request would hold a connection through the entire payment round-trip (seconds), and the throughput ceiling would be the slowest dependency. Industry standard for flash-sale systems (Tmall, KKTIX, Ticketmaster).

**Idempotency**: include `Idempotency-Key: <ASCII-printable, ≤128 chars>` header on `POST /api/v1/book` for at-most-once semantics. A replay returns the original 202 response (same `order_id`) with header `X-Idempotency-Replayed: true`. Cache TTL: 24h. **Stripe-style fingerprint check (N4)**: same key with a *different* body returns **409 Conflict** instead of replaying — this prevents a client mistake (reusing a key across logically-distinct requests) from silently returning the wrong response. 4xx validation errors are NOT cached, so a typo'd body doesn't burn the key for 24h. See [docs/PROJECT_SPEC.md §5](docs/PROJECT_SPEC.md) for the full contract table.

**The 404 window in practice**: typically <1 second on a healthy worker. Sustained 404s mean the worker is backed up — operators can verify via the `redis_stream_length{stream="orders:stream"}` metric or the `OrdersStreamBacklog*` alerts (see [docs/monitoring.md](docs/monitoring.md)).

## Development Commands

```bash
make build              # Build binary with race detection
make test               # Run tests with race detection
make lint               # Run golangci-lint
make mocks              # Generate mock files
make run-stress C=100 N=500   # Go stress test
make stress-k6 VUS=500 DURATION=30s  # K6 load test
make benchmark VUS=1000 DURATION=60s  # Full benchmark with report
make reset-db           # Reset database + Redis state
make migrate-up         # Run migrations
make migrate-down       # Revert last migration
make docker-restart     # Rebuild and restart app container
make curl-history PAGE=1 SIZE=5 STATUS=confirmed  # Query order history
```

## Observability

| Tool | URL | Purpose |
|------|-----|---------|
| Prometheus | `http://localhost:9090` | Metrics scraping |
| Grafana | `http://localhost:3000` (admin/admin) | 6-panel dashboard (RPS, p99/p95/p50 latency, conversion, goroutines, memory) |
| Jaeger | `http://localhost:16686` | Distributed tracing |

**Key Metrics**: `bookings_total`, `http_request_duration_seconds`, `worker_orders_total`, `inventory_conflicts_total`, `page_views_total`

## Docker Services

| Service | Port | Description |
|---------|------|-------------|
| app | 8080 | Booking API server |
| nginx | 80 | Reverse proxy + rate limiter |
| payment_worker | - | Kafka consumer for payments (`booking-cli payment`) |
| recon | - | Reconciler subcommand for stuck `charging` orders (`booking-cli recon`) |
| postgres | 5433 | PostgreSQL database |
| redis | 6379 | Cache + streams |
| kafka | 9092 | Event streaming |
| zookeeper | 2181 | Kafka coordination |
| prometheus | 9090 | Metrics collection |
| grafana | 3000 | Dashboards |
| jaeger | 16686/4317 | Tracing |

## Configuration

**YAML** (`config/config.yml`) with environment variable overrides:

| Config | Default | Env Var |
|--------|---------|---------|
| Server port | 8080 | PORT |
| Redis address | localhost:6379 | REDIS_ADDR |
| Kafka brokers | localhost:9092 | KAFKA_BROKERS |
| DB URL | postgres://user:password@localhost:5433/booking | DATABASE_URL |
| Log level | info | LOG_LEVEL |

## Performance

Architecture-evolution snapshot (early phases — for narrative, not current numbers):

| Configuration | RPS | P99 Latency |
|---------------|-----|-------------|
| Postgres only | ~4,000 | ~500ms |
| + Redis hot inventory | ~11,000 | ~50ms |
| + Kafka outbox | ~9,000 | ~100ms |
| + Saga compensation | ~8,500 | ~120ms |

**Current baseline** (post-PR #45, GC-tuned, c=500 VUs, 60s, 500k ticket pool, direct `app:8080`): **~54,000 RPS / p95 ~12.6ms** ([20260428_225152_compare_c500_a4_charging_intent](docs/benchmarks/20260428_225152_compare_c500_a4_charging_intent/comparison.md)). The early-phase numbers above predate the GC tuning landed in PRs #14/#15 (`GOGC=400`, `GOMEMLIMIT=256MiB`, sync.Pool for Lua args). All run-to-run reports live in [docs/benchmarks/](docs/benchmarks/); see [CLAUDE.md "Benchmark Conventions"](.claude/CLAUDE.md) for the apples-to-apples standard config.

## Documentation

- [Project Specification](docs/PROJECT_SPEC.md) - Comprehensive system spec
- [Post-Phase-2 Roadmap](docs/post_phase2_roadmap.md) - **Active sprint plan + Pattern A demo sequence** (canonical for "what's next")
- [Project Review Checkpoints](docs/checkpoints/) - Whole-project audit reports at phase boundaries
- [Scaling Roadmap](docs/scaling_roadmap.md) - Historical Stage 1-4 architecture evolution narrative
- [Architecture (Current)](docs/architecture/current_monolith.md) - Mermaid diagram
- [Architecture (Future)](docs/architecture/future_robust_monolith.md) - Target architecture
- [ADR-001: Queue Selection](docs/adr/0001_async_queue_selection.md) - Redis Streams vs Kafka
- [Phase 2 Review](docs/reviews/phase2_review.md) - Early-phase Redis integration review
- [Benchmarks](docs/benchmarks/) - timestamped performance reports
