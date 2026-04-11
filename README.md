# Booking Monitor System

> 中文版本: [README.zh-TW.md](README.zh-TW.md)

A high-concurrency ticket booking system simulation designed for flash sale scenarios (100k+ concurrent users). Evolves from direct DB usage to advanced caching, queueing, and saga patterns.

## Architecture

```
Client -> Nginx (rate limit) -> Gin API -> Redis Lua (atomic deduct)
  -> Redis Stream -> Worker -> PostgreSQL TX [Order + Outbox]
    -> OutboxRelay -> Kafka (order.created)
      -> PaymentWorker -> Success: confirmed | Failure: order.failed
        -> SagaCompensator -> DB + Redis inventory rollback
```

**Design**: Domain-Driven Design + Clean Architecture (Modular Monolith)

```
cmd/booking-cli/          # CLI entry point (server, stress, payment commands)
internal/
  domain/                 # Entities (Event, Order), repository interfaces
  application/            # Services: BookingService, WorkerService, OutboxRelay, SagaCompensator
  infrastructure/
    api/                  # Gin HTTP handlers + middleware
    cache/                # Redis: inventory, streams, idempotency, Lua scripts
    persistence/postgres/ # Repositories, UoW, advisory locks
    messaging/            # Kafka publisher + consumers
    observability/        # Prometheus metrics, OTEL tracing
    payment/              # Mock payment gateway
    config/               # YAML config + env overrides
pkg/logger/               # Structured logging (Zap)
deploy/                   # Postgres migrations, Redis, Nginx, Prometheus, Grafana configs
```

## Features

- **Dual-Tier Inventory**: Redis (hot path, sub-ms) + PostgreSQL (source of truth)
- **Async Processing**: Redis Streams with consumer groups and PEL recovery
- **Transactional Outbox**: Atomic order + event persistence, Kafka publishing
- **Saga Compensation**: Idempotent payment failure rollback (DB + Redis)
- **Idempotency**: 3 levels - API (header), worker (DB constraint), saga (Redis SETNX)
- **Rate Limiting**: Nginx (100 req/s/IP, burst 200)
- **Leader Election**: PostgreSQL advisory locks for single OutboxRelay instance
- **Full Observability**: Prometheus metrics, Grafana dashboards, Jaeger tracing, Zap logging
- **Correlation IDs**: End-to-end request tracking across all components

## Prerequisites

- Go 1.24+
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
| POST | `/api/v1/book` | Book tickets `{ user_id, event_id, quantity }` |
| GET | `/api/v1/history` | Order history `?page=1&size=10&status=confirmed` |
| POST | `/api/v1/events` | Create event `{ name, total_tickets }` |
| GET | `/api/v1/events/:id` | View event details |
| GET | `/metrics` | Prometheus metrics |

**Idempotency**: Include `Idempotency-Key: <uuid>` header on POST /book for at-most-once semantics.

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
| Grafana | `http://localhost:3000` (admin/admin) | 6-panel dashboard (RPS, latency, conversion, fairness, saturation) |
| Jaeger | `http://localhost:16686` | Distributed tracing |

**Key Metrics**: `bookings_total`, `http_request_duration_seconds`, `worker_orders_total`, `inventory_conflicts_total`, `page_views_total`

## Docker Services

| Service | Port | Description |
|---------|------|-------------|
| app | 8080 | Booking API server |
| nginx | 80 | Reverse proxy + rate limiter |
| payment_worker | - | Kafka consumer for payments |
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

| Configuration | RPS | P99 Latency |
|---------------|-----|-------------|
| Postgres only | ~4,000 | ~500ms |
| + Redis hot inventory | ~11,000 | ~50ms |
| + Kafka outbox | ~9,000 | ~100ms |
| + Saga compensation | ~8,500 | ~120ms |

## Documentation

- [Project Specification](docs/PROJECT_SPEC.md) - Comprehensive system spec
- [Scaling Roadmap](docs/scaling_roadmap.md) - Stage 1-4 evolution plan
- [Architecture (Current)](docs/architecture/current_monolith.md) - Phase 7.7 diagram
- [Architecture (Future)](docs/architecture/future_robust_monolith.md) - Target architecture
- [ADR-001: Queue Selection](docs/adr/0001_async_queue_selection.md) - Redis Streams vs Kafka
- [Phase 2 Review](docs/reviews/phase2_review.md) - Redis integration review
- [Benchmarks](docs/benchmarks/) - 15 timestamped performance reports
