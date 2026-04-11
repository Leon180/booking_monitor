# Booking Monitor - Project Specification

> 中文版本: [PROJECT_SPEC.zh-TW.md](PROJECT_SPEC.zh-TW.md)

## 1. Overview

A high-concurrency ticket booking system designed to simulate "flash sale" scenarios (100k+ concurrent users). Built with Go using Domain-Driven Design and Clean Architecture principles.

**Goal**: Prevent overselling through a multi-layer strategy while maximizing throughput.

**Development Timeline**: Feb 14 - Feb 24, 2026 (10 days, 15 commits, 12 phases)

---

## 2. System Architecture

```
Client --> Nginx (rate limit: 100 req/s/IP, burst 200)
  --> Gin API (idempotency check, correlation ID, metrics)
    --> BookingService
      --> Redis Lua Script (atomic DECRBY + XADD to stream)
        --> Redis Stream (orders:stream)
          --> WorkerService (consumer group, PEL recovery)
            --> PostgreSQL TX [Order + OutboxEvent]
              --> OutboxRelay (advisory lock leader election)
                --> Kafka (order.created)
                  --> PaymentWorker
                    --> Success: UPDATE status='confirmed'
                    --> Failure: Outbox -> Kafka (order.failed)
                      --> SagaCompensator
                        --> DB: IncrementTicket + status='compensated'
                        --> Redis: SETNX + INCRBY (idempotent revert)
```

### Data Flow Summary

| Step | Component | Storage | Pattern |
|------|-----------|---------|---------|
| 1 | API receives booking | Redis | Lua atomic deduction |
| 2 | Worker processes order | PostgreSQL | UoW (Order + Outbox in same TX) |
| 3 | OutboxRelay publishes | Kafka | Transactional outbox |
| 4 | Payment processes | PostgreSQL | Status update |
| 5 | On failure: compensate | PostgreSQL + Redis | Saga pattern |

---

## 3. Domain Model

### Entities

**Event** (`internal/domain/event.go`)
```
ID, Name, TotalTickets, AvailableTickets, Version
Invariant: 0 <= AvailableTickets <= TotalTickets
Method: Deduct(quantity) - validates capacity before decrement
```

**Order** (`internal/domain/order.go`)
```
ID, EventID, UserID, Quantity, Status, CreatedAt
Status lifecycle: pending -> confirmed | pending -> failed -> compensated
Constraint: UNIQUE(user_id, event_id) WHERE status != 'failed'
  (partial index - allows retry after payment failure)
```

**OutboxEvent** (`internal/domain/event.go`)
```
ID, EventType, Payload (JSON), Status, ProcessedAt
Types: order.created, order.failed
```

### Domain Interfaces

| Interface | Purpose | Implementation |
|-----------|---------|----------------|
| EventRepository | Event CRUD + DecrementTicket/IncrementTicket | PostgreSQL |
| OrderRepository | Order CRUD + status updates | PostgreSQL |
| OutboxRepository | Outbox CRUD + ListPending/MarkProcessed | PostgreSQL |
| InventoryRepository | Hot inventory deduction/reversion | Redis (Lua scripts) |
| OrderQueue | Async order stream (Enqueue/Dequeue/Ack) | Redis Streams |
| IdempotencyRepository | Request deduplication (24h TTL) | Redis |
| EventPublisher | Publish domain events | Kafka |
| PaymentGateway | Charge payments | Mock (configurable success rate) |
| DistributedLock | Leader election | PostgreSQL advisory locks |
| UnitOfWork | Transaction management | PostgreSQL |

---

## 4. Database Schema

### PostgreSQL (port 5433)

```sql
-- events: Source of truth for inventory
CREATE TABLE events (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    total_tickets INT NOT NULL,
    available_tickets INT NOT NULL,
    version INT DEFAULT 0
);
-- Added in migration 000004:
ALTER TABLE events ADD CONSTRAINT check_available_tickets_non_negative
  CHECK (available_tickets >= 0);
-- Seeded: INSERT INTO events (name, total_tickets, available_tickets)
--        VALUES ('Jay Chou Concert', 100, 100);

-- orders: Booking transaction log
CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    event_id INT NOT NULL,
    user_id INT NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    status VARCHAR(50) NOT NULL,  -- pending, confirmed, failed, compensated
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
-- Migration 000004 added UNIQUE(user_id, event_id) constraint.
-- Migration 000006 replaced it with a partial unique index to allow
-- retry after payment failure:
CREATE UNIQUE INDEX uq_orders_user_event ON orders (user_id, event_id)
  WHERE status != 'failed';

-- events_outbox: Transactional outbox for event publishing
CREATE TABLE events_outbox (
    id SERIAL PRIMARY KEY,
    event_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(20) DEFAULT 'PENDING',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    processed_at TIMESTAMPTZ  -- added in migration 000005
);
```

### Migration History (6 files in `deploy/postgres/migrations/`)

| # | Purpose |
|---|---------|
| 000001 | Create `events` table + seed "Jay Chou Concert" (100 tickets) |
| 000002 | Create `orders` table |
| 000003 | Create `events_outbox` table |
| 000004 | Add `check_available_tickets_non_negative` + `UNIQUE(user_id, event_id)` |
| 000005 | Add `processed_at` column to `events_outbox` |
| 000006 | Replace unique constraint with partial index `WHERE status != 'failed'` — allows users to retry purchase after payment failure |

### Redis

| Key Pattern | Type | Purpose |
|-------------|------|---------|
| `event:{id}:qty` | String (integer) | Hot inventory counter |
| `orders:stream` | Stream | Async order queue |
| `idempotency:{key}` | String | Request deduplication (24h TTL) |
| `saga:reverted:{order_id}` | String | Compensation idempotency (7d TTL) |

### Kafka Topics

| Topic | Producer | Consumer Group | Consumer | Payload |
|-------|----------|----------------|----------|---------|
| `order.created` | OutboxRelay | `payment-service-group-test` | PaymentWorker | OrderCreatedEvent (id, user_id, event_id, quantity, amount) |
| `order.failed` | PaymentWorker (via outbox) | `booking-saga-group` | SagaCompensator | OrderFailedEvent (order_id, event_id, user_id, quantity, reason) |

---

## 5. API Reference

### POST /api/v1/book
Book tickets for an event.
```json
// Request
{ "user_id": 123, "event_id": 1, "quantity": 1 }
// Headers: Idempotency-Key: <uuid> (optional)

// 200 OK
{ "message": "booking successful" }
// 409 Conflict
{ "error": "sold out" }
// 409 Conflict
{ "error": "user already bought ticket" }
```

### GET /api/v1/history
Paginated order history.
```
?page=1&size=10&status=confirmed
```

### POST /api/v1/events
Create a new event.
```json
{ "name": "Concert", "total_tickets": 1000 }
```

### GET /api/v1/events/:id
View event details. Increments `page_views_total` metric for conversion tracking.

### GET /metrics
Prometheus metrics endpoint.

### POST /book (legacy)
Direct booking endpoint (non-versioned, retained from Phase 0).

---

## 6. Infrastructure Patterns

### 6.1 Redis Lua Scripts (Atomic Operations)

**deduct.lua** - Inventory deduction + stream publish
```
1. DECRBY event:{id}:qty by quantity
2. If result < 0: INCRBY to revert, return -1 (sold out)
3. XADD orders:stream with order metadata
4. Return 1 (success)
```

**revert.lua** - Idempotent compensation
```
1. SETNX saga:reverted:{order_id} (prevents double-revert)
2. If set: EXPIRE 7d + INCRBY event:{id}:qty
3. Return 1 (reverted) or 0 (already reverted)
```

### 6.2 Transactional Outbox

1. Worker writes `Order` + `OutboxEvent` in same PostgreSQL transaction
2. OutboxRelay (background goroutine) polls `events_outbox WHERE processed_at IS NULL` every 500ms
3. Publishes batch (up to 100) to Kafka
4. Marks each event as processed
5. Leader election via `pg_try_advisory_lock(1001)` - only 1 instance publishes

### 6.3 Saga Compensation

1. PaymentWorker consumes `order.created`, calls PaymentGateway.Charge()
2. On failure: updates order status to `failed`, inserts `order.failed` outbox event
3. SagaCompensator consumes `order.failed`:
   - DB: IncrementTicket + update order status to `compensated` (same TX)
   - Redis: SETNX-guarded INCRBY to restore hot inventory
4. Max 3 retries, then skip (prevents partition blocking)

### 6.4 Unit of Work

`PostgresUnitOfWork` wraps transactions via context injection:
- `Do(ctx, fn)` begins TX, stores in context, calls fn, commits/rollbacks
- Repositories extract TX from context via `txKey`
- Ensures Order + Outbox writes are atomic

### 6.5 Worker Service

- Reads from Redis Stream (`orders:stream`) via consumer group (`orders:group`)
- PEL (Pending Entries List) recovery on startup for crash resilience
- 3 retries with linear backoff (100ms * attempt)
- On exhausted retries: revert Redis inventory + move to DLQ stream (`orders:dlq`) + ACK
- Self-healing: recreates consumer group on NOGROUP error (e.g. after Redis FLUSHALL)
- Transaction body (UnitOfWork): `DecrementTicket` (DB double-check) → `Create Order` → `Create OutboxEvent` → COMMIT
- Per-message metrics: `success`, `sold_out`, `duplicate`, `db_error` outcomes + processing duration

### 6.6 Idempotency (3 Levels)

| Level | Mechanism | Scope |
|-------|-----------|-------|
| API | `Idempotency-Key` header -> Redis cache (24h TTL) | Duplicate HTTP requests |
| Worker | Partial unique index on `(user_id, event_id) WHERE status != 'failed'` | Duplicate orders (allows retry after payment failure) |
| Saga | `SETNX saga:reverted:{order_id}` in Redis | Duplicate compensations |

---

## 7. Observability

### Metrics (Prometheus)
| Metric | Type | Description |
|--------|------|-------------|
| `http_requests_total` | Counter | All requests by method/path/status |
| `http_request_duration_seconds` | Histogram | Request latency (p99-optimized buckets) |
| `bookings_total` | Counter | Booking outcomes (success, sold_out, duplicate, error) |
| `worker_orders_total` | Counter | Worker processing by outcome |
| `worker_processing_duration_seconds` | Histogram | Worker latency |
| `inventory_conflicts_total` | Counter | Redis-approved-but-DB-rejected oversells |
| `page_views_total` | Counter | Event page views (conversion funnel) |

### Tracing (OpenTelemetry + Jaeger)
- Decorator pattern: `BookingServiceTracingDecorator`, `WorkerServiceMetricsDecorator`, `OutboxRelayTracingDecorator`
- GRPC exporter to Jaeger (port 4317)
- Always-sample policy

### Logging (Zap)
- Structured JSON to stdout
- Correlation ID injection via middleware
- Component-scoped loggers

### Dashboards (Grafana)
Pre-provisioned 6-panel dashboard: RPS, Latency Quantiles, Conversion Rate, IP Fairness, Saturation

---

## 8. Development Phases (Complete History)

| Phase | Date | Commit | Description |
|-------|------|--------|-------------|
| 0 | Feb 14 | `65502bb` | Basic booking API + Postgres + Prometheus/Grafana/Jaeger + CLI |
| 1 | Feb 15 | `67234b4`, `f9ff381` | K6 load testing + scaling roadmap + benchmark automation |
| 2 | Feb 15 | `65058a9` | Redis hot inventory (Lua scripts, 4k->11k RPS) |
| 3 | Feb 16 | `fefa372` | Centralized YAML config + Lua script hardening + HTTP 409 |
| 4 | Feb 17 | `1e80723` | X-Correlation-ID middleware + panic recovery |
| 5 | Feb 17 | `96d8f51` | Redis Streams async queue + WorkerService + ADR-001 |
| 6 | Feb 17 | `df38baa` | Idempotency (API + worker) + Unit of Work pattern |
| 7 | Feb 18 | `9bd9b2b` | Kafka + outbox pattern (8% reliability overhead) |
| 8 | Feb 18 | `51cdeb5` | Payment service (mock gateway) + E2E flow validation |
| 9 | Feb 19 | `1caa7a1`, `a966f45` | Parametrized workers + comprehensive unit tests |
| 10 | Feb 20 | `572d430` | Nginx API gateway + rate limiting + observability refinements |
| 11 | Feb 21 | `f56ab82` | PostgreSQL advisory locks for OutboxRelay leader election |
| 12 | Feb 24 | `4e89ff7` | Saga compensation + idempotent Redis rollback + partial unique index (allows retry after payment failure) |

---

## 9. Performance Benchmarks

| Configuration | RPS | P99 Latency | Bottleneck |
|---------------|-----|-------------|------------|
| Stage 1: Postgres only | ~4,000 | ~500ms | DB CPU (212%) |
| Stage 2: Redis hot inventory | ~11,000 | ~50ms | Memory/Network |
| Stage 2 + Kafka outbox | ~9,000 | ~100ms | Kafka throughput |
| Full system (with saga) | ~8,500 | ~120ms | Saga overhead |

Benchmark reports in `docs/benchmarks/` (15 timestamped directories).

---

## 10. Remaining Roadmap

### High Priority
- **DLQ Worker**: Implement dead letter retry policy with configurable backoff and max attempts. Currently messages that fail 3x in the worker are moved to `orders:dlq` but never reprocessed.

### Medium Priority
- **Event Sourcing**: Replace direct DB mutations with an append-only event store for full audit trail and replay capability.
- **CQRS Read Model**: Separate read projections optimized for query patterns (history, analytics).
- **Horizontal Scaling Tests**: Validate multi-instance deployment with Nginx load balancing, verify advisory lock leader election works across instances.

### Low Priority
- **Real Payment Gateway**: Replace mock with Stripe/PayPal integration, handle webhooks.
- **Admin Dashboard**: Management UI for events, orders, system health.
- **Stage 4 Sharding**: Distributed DB sharding for geo-distributed events.

---

## 11. File Reference

### Entry Points
| File | Purpose |
|------|---------|
| `cmd/booking-cli/main.go` | CLI entry: `server`, `stress`, `payment` commands |
| `cmd/verify-redis/main.go` | Redis verification utility |

### Domain
| File | Purpose |
|------|---------|
| `internal/domain/event.go` | Event entity + OutboxEvent + Kafka event types |
| `internal/domain/order.go` | Order entity + status constants |
| `internal/domain/repositories.go` | Repository interfaces |
| `internal/domain/inventory.go` | InventoryRepository interface |
| `internal/domain/queue.go` | OrderQueue interface |
| `internal/domain/messaging.go` | EventPublisher interface |
| `internal/domain/payment.go` | PaymentGateway + PaymentService interfaces |
| `internal/domain/lock.go` | DistributedLock interface |
| `internal/domain/idempotency.go` | IdempotencyRepository interface |
| `internal/domain/worker_metrics.go` | WorkerMetrics interface |
| `internal/domain/uow.go` | UnitOfWork interface |

### Application Services
| File | Purpose |
|------|---------|
| `internal/application/booking_service.go` | Core booking logic (Redis deduction) |
| `internal/application/worker_service.go` | Background order processing from streams |
| `internal/application/outbox_relay.go` | Transactional outbox -> Kafka publisher |
| `internal/application/saga_compensator.go` | Payment failure compensation |
| `internal/application/payment/service.go` | Payment processing logic |

### Infrastructure
| File | Purpose |
|------|---------|
| `internal/infrastructure/api/handler.go` | HTTP handlers + route registration |
| `internal/infrastructure/api/middleware/` | Idempotency, correlation ID, metrics, tracing |
| `internal/infrastructure/cache/redis.go` | Redis inventory + idempotency repos |
| `internal/infrastructure/cache/redis_queue.go` | Redis Streams consumer |
| `internal/infrastructure/cache/lua/deduct.lua` | Atomic inventory deduction script |
| `internal/infrastructure/cache/lua/revert.lua` | Idempotent compensation script |
| `internal/infrastructure/persistence/postgres/repositories.go` | DB repositories |
| `internal/infrastructure/persistence/postgres/uow.go` | Unit of Work implementation |
| `internal/infrastructure/persistence/postgres/advisory_lock.go` | Distributed lock |
| `internal/infrastructure/messaging/kafka_publisher.go` | Kafka event publisher |
| `internal/infrastructure/messaging/kafka_consumer.go` | Payment Kafka consumer |
| `internal/infrastructure/messaging/saga_consumer.go` | Saga Kafka consumer |
| `internal/infrastructure/observability/metrics.go` | Prometheus metrics setup |
| `internal/infrastructure/config/config.go` | YAML config + env overrides |
| `internal/infrastructure/payment/mock_gateway.go` | Mock payment gateway |

### Configuration & Deployment
| File | Purpose |
|------|---------|
| `config/config.yml` | Application configuration |
| `docker-compose.yml` | Full stack orchestration (10 services) |
| `Dockerfile` | Multi-stage build (alpine, ~7MB) |
| `deploy/postgres/migrations/` | 6 SQL migration files |
| `deploy/redis/redis.conf` | Redis AOF persistence config |
| `deploy/nginx/nginx.conf` | Rate limiting + reverse proxy |
| `deploy/prometheus/prometheus.yml` | Scrape config (5s interval) |
| `deploy/prometheus/alerts.yml` | Alert rules |
| `deploy/grafana/provisioning/` | Pre-configured datasources + dashboards |

### Documentation
| File | Purpose |
|------|---------|
| `docs/scaling_roadmap.md` | Stage 1-4 evolution plan |
| `docs/architecture/current_monolith.md` | Phase 7.7 Mermaid diagram |
| `docs/architecture/future_robust_monolith.md` | Phases 8-11 target architecture |
| `docs/adr/0001_async_queue_selection.md` | Redis Streams vs Kafka decision |
| `docs/reviews/phase2_review.md` | Redis integration review |
| `docs/benchmarks/` | 15 timestamped performance reports |
