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
    Worker -->|UoW: INSERT order| PG[(Postgres<br/>source of truth)]
    Client -->|POST /pay| API
    API -->|CreatePaymentIntent| Gateway[Payment<br/>Gateway]
    Gateway -.->|webhook<br/>signed| API
    API -->|UoW: MarkPaid OR<br/>MarkFailed + outbox| PG
    Sweeper[Expiry<br/>Sweeper] -->|UoW: MarkExpired<br/>+ outbox| PG
    PG -.->|advisory-lock<br/>leadered relay| Kafka{{Kafka}}
    Kafka -.->|order.failed| Saga[Saga Consumer<br/>in-process]
    Saga --> Redis
    Saga --> PG
```

> Solid arrows = synchronous hot path · dashed arrows = async / event-driven
> See [v0.4.0 release notes](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0) for the cache-truth contract: Redis ephemeral, Postgres source-of-truth, drift detected and named.

**Design**: Domain-Driven Design + Clean Architecture (Modular Monolith)

```
cmd/booking-cli/          # CLI entry: server, stress, recon, saga-watchdog, expiry-sweeper subcommands
internal/
  domain/                 # Entities (Event, Order, OutboxEvent), value types (StuckCharging, StuckFailed), repository interfaces
  application/            # Cross-package fx module + UnitOfWork interface + wire-format event DTOs (order_events.go)
    booking/              # POST /book hot path: BookTicket validates + Redis Lua deduct
    worker/               # Order-stream consumer + queue policy + per-message processor
    outbox/               # Outbox relay polling + Kafka publish (transactional outbox impl)
    event/                # Event creation + Redis hot inventory provisioning
    payment/              # /pay (D4) handler + gateway adapter + D5 webhook
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
    payment/              # Mock payment gateway (CreatePaymentIntent + GetStatus; success/failure driven by D5 webhook outcomes, post-D7)
    config/               # YAML config + env overrides (cleanenv)
  log/                    # Structured logging (Zap) — context propagation, typed tags, runtime level
  bootstrap/              # fx wiring for logger + tracer + DI primitives
deploy/                   # Postgres migrations (15), Redis Lua, Nginx, Prometheus alerts, Grafana dashboards
```

### Architecture Evolution

The architecture didn't appear all at once. Each layer was added in response to a benchmark-documented bottleneck — the four-stage progression below tells the v0.1.0→v0.4.0 evolution story (Stage 4 = the v0.2.0–v0.4.0 milestone *before* v0.6.0's D7 narrowed it). Walk through it commit-by-commit on the [Releases page](https://github.com/Leon180/booking_monitor/releases); the [top-level diagram](#architecture) above shows the current post-D7 shape.

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

**Stage 4 — full event-driven (v0.2.0–v0.4.0; pre-D7 historical).** Worker writes order + outbox in one UoW; advisory-lock-leadered relay pushes to Kafka; payment service + saga compensator are independent Kafka consumers. This is the architecture released as [v0.2.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.2.0) and hardened through [v0.3.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.3.0) + [v0.4.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0). [v0.6.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.6.0)'s D7 then narrowed this — worker writes only the order row (no `order.created` outbox emit), the `payment_worker` binary is gone, and the saga consumer/compensator runs in-process inside `app`. See the [top-level diagram](#architecture) for the current shape; the diagram below is preserved as the v0.2.0–v0.4.0 milestone reference (and what `cmd/booking-cli-stage4/` will benchmark in D12).

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

### Pattern A flow (shipped in v0.5.0 + v0.6.0)

`POST /book` is split into reservation + explicit `POST /pay` + `POST /webhook/payment` — Stripe Checkout / KKTIX shape. The flow below runs end-to-end against the Docker stack.

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
    Note over C,P: Reservation (D1-D3, D4.1)
    C->>+API: POST /book {ticket_type_id, qty}
    API->>+R: Lua DECRBY + XADD<br/>(price snapshot from ticket_type)
    R-->>-API: ok
    API-->>-C: 202 {order_id, status:reserved,<br/>reserved_until, links.pay}
    R-->>W: XReadGroup
    W->>P: INSERT order status=awaiting_payment<br/>(amount_cents + currency frozen)
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
        SW->>P: SELECT awaiting_payment<br/>WHERE reserved_until <= NOW() - grace
        P-->>SW: overdue rows
        SW->>P: UoW {MarkExpired + emit order.failed}
        Note over P,R: outbox relay → Kafka order.failed →<br/>saga consumer + compensator<br/>(in-process inside app, post-D7)<br/>runs revert.lua INCRBY → MarkCompensated
        end
    end
```

D6's job is timing — when does the row expire. The saga compensator owns inventory revert (idempotent via `saga:reverted:order:<id>` SETNX). Same shape as D5's failure path; D6 doesn't call `revert.lua` directly.

**Shipped:** [`v0.5.0`](https://github.com/Leon180/booking_monitor/releases/tag/v0.5.0) (2026-05-07, D1–D6) closes the reservation→payment→expiry loop above. [`v0.6.0`](https://github.com/Leon180/booking_monitor/releases/tag/v0.6.0) (2026-05-08) narrows the saga compensator scope (D7 — deletes the legacy A4 auto-charge path; `order.failed` topic now has only the D5 webhook + D6 sweeper as production emitters) and ships the [browser demo](demo/) (D8-minimal).

## Features

- **Dual-Tier Inventory**: Redis (hot path, sub-ms) + PostgreSQL (source of truth)
- **Async Processing**: Redis Streams with consumer groups and PEL recovery
- **Transactional Outbox**: Atomic order + event persistence, Kafka publishing
- **Saga Compensation**: Idempotent payment-failure + reservation-expiry rollback (DB + Redis)
- **Idempotency**: 4 levels — API (`Idempotency-Key` header + N4 fingerprint validation), worker (DB UNIQUE constraint), saga (Redis SETNX), payment gateway (mock implements idempotent `CreatePaymentIntent`)
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

4. **Reset State** (for testing):
   ```bash
   make reset-db
   ```

## Browser Demo (D8-minimal)

A single-page Vite + React + TS app at [demo/](demo/) exercises the full Pattern A flow (book → pay-or-let-expire → terminal) end-to-end against the running stack. Mock-only — confirm step goes through `POST /test/payment/confirm/:order_id` (forges a signed webhook), not a real Stripe gateway. Real-Stripe wiring is deferred to D4.2.

```bash
# 1. Start the API stack with CORS + test endpoints enabled
# APP_ENV=development is required: docker-compose.yml passes
# APP_ENV=${APP_ENV:-production} into the container, which wins over
# config/config.yml (cleanenv precedence: env > yaml). Without this
# the container starts as production and rejects ENABLE_TEST_ENDPOINTS.
export APP_ENV=development
export CORS_ALLOWED_ORIGINS=http://localhost:5173,http://127.0.0.1:5173
export ENABLE_TEST_ENDPOINTS=true
export PAYMENT_WEBHOOK_SECRET=demo_secret_local_only
docker compose up -d

# 2. Run the demo dev server
cd demo
npm install   # first time only — Node ≥ 20.19
npm run dev   # http://localhost:5173
```

See [demo/README.md](demo/README.md) for the full flow walkthrough, intent-aware display rationale ([demo/src/intent.ts](demo/src/intent.ts)), and the `(intent, observed_status) → display` mapping.

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/book` | Submit a booking. Returns **202 Accepted** + an `order_id` to track it (see **Booking flow** below). |
| GET | `/api/v1/orders/:id` | Look up the latest status of one order by `order_id`. May return 404 in a brief window after `POST /book` (see **Booking flow**). |
| POST | `/api/v1/orders/:id/pay` | **D4** Pattern A — create a Stripe-shape `PaymentIntent` for a reservation. Returns 200 with `{order_id, payment_intent_id, client_secret, amount_cents, currency}`. Idempotent on `order_id` (gateway-side). Status guards: 409 if not `awaiting_payment`, 409 if reservation expired, 404 if order not found. |
| GET | `/api/v1/history` | Order history `?page=1&size=10&status=confirmed` |
| POST | `/api/v1/events` | Create event `{ name, total_tickets, price_cents, currency }`. D4.1 atomically provisions a default ticket_type carrying the price snapshot; the response surfaces `ticket_types[].id` for booking. |
| GET | `/api/v1/events/:id` | **Stub** — returns `{"message": "View event", "event_id": ...}` + bumps `page_views_total` for conversion tracking. Does NOT load event details (deferred to Phase 3 demo). |
| POST | `/webhook/payment` | **D5** Pattern A — inbound payment-provider webhook (Stripe-shape envelope). Verifies HMAC-SHA256 signature against `PAYMENT_WEBHOOK_SECRET`, dispatches on event type: `payment_intent.succeeded` → MarkPaid; `payment_intent.payment_failed` → MarkPaymentFailed + emit `order.failed` (saga compensation). Idempotent on duplicate provider redelivery via DB terminal status. Mounted at the engine root (NOT under `/api/v1`); auth is the signature, not network. |
| POST | `/test/payment/confirm/:order_id` | **D5 (test-only)** Mock provider's webhook emit — gated by `ENABLE_TEST_ENDPOINTS` (off by default in prod). Reads the order's `payment_intent_id`, builds a Stripe-shape envelope with `metadata.order_id`, signs it with the same webhook secret, and POSTs to `/webhook/payment`. Used by integration tests + dev demos to drive the full pipeline without a real provider. Query: `?outcome=succeeded\|failed`. |
| GET | `/metrics` | Prometheus metrics |
| GET | `/livez` | Liveness probe — always 200 if process is up (no downstream deps) |
| GET | `/readyz` | Readiness probe — 200 only if PG + Redis + Kafka all answer within 1s; 503 + per-dep JSON otherwise |

### Booking flow

`POST /api/v1/book` is **asynchronous by design**. The 202 (not 200) is honest: at the moment of the response, only the Redis-side inventory deduct has happened — the order hasn't been written to DB, payment hasn't been attempted, the booking isn't actually confirmed yet. The client gets back an `order_id` and is expected to poll for the terminal status.

```
1. Client → POST /api/v1/book { user_id, ticket_type_id, quantity }
2. Server → 202 Accepted {
       order_id:           "019dd493-47ae-79b1-b954-8e0f14a6a482",
       status:             "reserved",
       message:            "booking accepted, complete payment before reserved_until",
       reserved_until:     "2026-05-04T22:30:00Z",
       expires_in_seconds: 900,
       links: {
         self: "/api/v1/orders/019dd493-...",
         pay:  "/api/v1/orders/019dd493-.../pay"
       }
   }

   At this point:
   - Redis inventory: deducted (the "load-shed gate")
   - DB orders row:   not yet (worker writes it ~ms later as awaiting_payment,
                       with amount_cents + currency snapshot frozen onto the row)
   - Payment:         not attempted yet — client must POST links.pay
                       before reserved_until or the D6 expiry sweeper
                       flips the row to "expired" and reverts inventory
   - Outcome:         not yet known

3. Client → GET /api/v1/orders/<order_id>  (poll with backoff: 100ms → 250ms → 500ms ...)

   Possible responses:
   - 404  → worker hasn't persisted the row yet. Retry.
   - 200  → { id, user_id, ticket_type_id, quantity, amount_cents, currency,
              status, reserved_until, created_at }
            where `status` is one of:
              "awaiting_payment" — DB persisted, awaiting POST /pay before reserved_until
              "charging"         — PaymentIntent created, payment in progress
              "paid"             — paid + booked                       ✓ terminal (success)
              "expired"          — reserved_until elapsed without payment;
                                    inventory reverted                 ✓ terminal (timeout)
              "failed"           — payment failed; saga will compensate
              "compensated"      — saga has rolled back inventory      ✓ terminal (failure)
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
| app | 8080 | Booking API server (also hosts the in-process saga consumer for `order.failed`) |
| nginx | 80 | Reverse proxy + rate limiter |
| recon | - | Reconciler subcommand for stuck `charging` orders (`booking-cli recon`) |
| saga_watchdog | - | DB-side sweep for stuck `failed` orders (`booking-cli saga-watchdog`) |
| expiry_sweeper | - | D6 reservation-expiry sweeper (`booking-cli expiry-sweeper`) |
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
- [Browser Demo](demo/README.md) - **D8-minimal** Vite + React + TS app: book → pay → confirm/let-expire end-to-end
- [Project Review Checkpoints](docs/checkpoints/) - Whole-project audit reports at phase boundaries
- [Scaling Roadmap](docs/scaling_roadmap.md) - Historical Stage 1-4 architecture evolution narrative
- [Architecture (Current)](docs/architecture/current_monolith.md) - Mermaid diagram
- [Architecture (Future)](docs/architecture/future_robust_monolith.md) - Target architecture
- [ADR-001: Queue Selection](docs/adr/0001_async_queue_selection.md) - Redis Streams vs Kafka
- [Phase 2 Review](docs/reviews/phase2_review.md) - Early-phase Redis integration review
- [Benchmarks](docs/benchmarks/) - timestamped performance reports
