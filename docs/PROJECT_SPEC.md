# Booking Monitor - Project Specification

> 中文版本: [PROJECT_SPEC.zh-TW.md](PROJECT_SPEC.zh-TW.md)

## 1. Overview

A high-concurrency ticket booking system designed to simulate "flash sale" scenarios (100k+ concurrent users). Built with Go using Domain-Driven Design and Clean Architecture principles.

**Goal**: Prevent overselling through a multi-layer strategy while maximizing throughput.

**Development Timeline**: Feb 14 - Feb 24, 2026 (10 days, 15 commits, 12 phases). A multi-agent review on Apr 11, 2026 surfaced 66 findings, remediated across PRs #8 (CRITICAL) / #9 (HIGH) / #12 (MEDIUM/LOW/NIT) / #13 (observability + smoke test plan). GC optimization followed on Apr 12–13 via PRs #14 (baseline harness + quick wins, +157% RPS) and #15 (deep fixes: sync.Pool, escape analysis, GOMEMLIMIT, combined middleware). See Section 8.

---

## 2. System Architecture

```
Client --> Nginx (rate limit: 100 req/s/IP, burst 200)
  --> Gin API (idempotency check, correlation ID, metrics, mapError)
    --> BookingService
      --> Redis Lua Script (atomic DECRBY + XADD to stream)
        --> Redis Stream (orders:stream)
          --> WorkerService (consumer group, PEL recovery)
            --> PostgreSQL TX [Order + OutboxEvent]
              --> OutboxRelay (advisory lock leader election)
                --> Kafka (order.created)
                  --> PaymentWorker (KafkaConsumer)
                    --> Success: UPDATE status='confirmed'
                    --> Invalid input: DLQ (order.created.dlq)
                    --> Failure: Outbox -> Kafka (order.failed)
                      --> SagaCompensator (Redis-backed retry counter)
                        --> DB: IncrementTicket + status='compensated'
                        --> Redis: INCRBY then SET NX EX (crash-safe revert)
                        --> Budget exhausted: DLQ (order.failed.dlq)
                                             + saga_poison_messages_total
```

### Data Flow Summary

The happy path and the failure path both start from a single
`POST /api/v1/book` call but diverge at step 4. Every boundary
crossing uses an at-least-once + idempotent contract; there is
never a synchronous RPC that blocks the API response on Kafka
or Postgres writes.

#### Happy path

| # | Component | Input | Storage touched | Effect | Failure behaviour |
|---|-----------|-------|-----------------|--------|-------------------|
| 1 | Gin API handler (`/api/v1/book`) | User request | Redis (idempotency key, 24h TTL) | `Idempotency-Key` header, if present, is looked up first — a hit replays the cached 2xx/4xx/5xx body verbatim and skips the rest | Missing/duplicate body → 400 `"invalid request body"`; `mapError` sanitizes any downstream leak |
| 2 | `BookingService.BookTicket` | `user_id, event_id, quantity` | Redis via `deduct.lua` (atomic) | `DECRBY event:{id}:qty` → if `>= 0` also `XADD orders:stream` → return 200; if `< 0` `INCRBY` revert and return 409 `sold out` | API returns immediately after Redis — the order is **not yet persisted**, just queued |
| 3 | `WorkerService.processMessage` (consumer group on `orders:stream`) | Stream message | PostgreSQL (single UoW transaction) | `DecrementTicket` (DB row-level double-check, guards against Redis/DB drift) → `orderRepo.Create` (UNIQUE partial index catches duplicate purchase) → `outboxRepo.Create(event_type="order.created")` — **all three in one tx** | `DecrementTicket` rejects → revert Redis + ACK (inventory conflict metric); `orderRepo.Create` hits `ErrUserAlreadyBought` → revert Redis + ACK (duplicate metric); other errors → no ACK, `processWithRetry` 3×, then DLQ (`orders:dlq`) + Redis revert |
| 4 | `OutboxRelay` (background goroutine, single leader elected via Postgres advisory lock 1001) | `events_outbox WHERE processed_at IS NULL` | PostgreSQL (read + update) → Kafka topic `order.created` | Polls every 500ms (partial index `events_outbox_pending_idx` covers this), publishes up to 100 events per tick, then `UPDATE processed_at = NOW()`. Publish failure → skip `MarkProcessed` so the next tick retries. MarkProcessed failure after a successful publish → event will be re-published on next tick, consumers MUST be idempotent | Leader crash → advisory lock auto-releases (session-bound) → a standby acquires it on its next tick |
| 5 | `KafkaConsumer` → `PaymentService.ProcessOrder` | `OrderCreatedEvent` | Redis (idempotency via `orderRepo.GetByID` → status check) → `PaymentGateway.Charge` → PostgreSQL (status update) | If order is already `confirmed`/`failed`/`compensated` → skip (idempotent). Otherwise charge mock gateway. On success: `UPDATE orders SET status='confirmed'`. Commit Kafka offset | Malformed JSON / `ErrInvalidPaymentEvent` → dead-letter to `order.created.dlq` with provenance headers + commit offset. Transient DB/Redis errors → do NOT commit, Kafka rebalance re-delivers |

At this point the user's booking is fully confirmed: Redis, DB, and payment status are consistent.

#### Failure path (payment gateway rejects the charge)

| # | Component | Input | Storage touched | Effect |
|---|-----------|-------|-----------------|--------|
| 5a | `PaymentService.ProcessOrder` (same call as step 5 above) | `Charge` returned error | PostgreSQL (single UoW tx) | `UPDATE orders SET status='failed'` + `outboxRepo.Create(event_type="order.failed")` — same tx, same guarantee as step 3's outbox |
| 5b | `OutboxRelay` | `order.failed` pending row | PostgreSQL → Kafka topic `order.failed` | Same polling loop as step 4, different topic |
| 5c | `SagaConsumer` → `SagaCompensator.HandleOrderFailed` | `OrderFailedEvent` | PostgreSQL (UoW tx: `IncrementTicket` + `status='compensated'`) → Redis via `revert.lua` (`INCRBY event:{id}:qty` → `SET saga:reverted:{order_id} NX EX 7d`) | DB path is idempotent via the `OrderStatusCompensated` guard; Redis path is idempotent via the `saga:reverted:*` key |
| 5d | On compensator error | — | Redis (durable retry counter `saga:retry:p{partition}:o{offset}` TTL 24h) | Counter incremented, message **not committed** so Kafka re-delivers. Counter survives consumer restart |
| 5e | After `sagaMaxRetries = 3` | Poison message | Kafka topic `order.failed.dlq` + metrics | Original payload + provenance headers written to DLQ, `saga_poison_messages_total` and `dlq_messages_total{topic, reason="max_retries"}` incremented, retry counter cleared, Kafka offset committed. **No silent drops** |

At this point the Redis and DB inventory are back to the pre-booking state; the user sees `status='compensated'` in their history.

#### Cross-cutting guarantees

- **API response never blocks on Kafka or Postgres writes.** Only Redis is in the synchronous path.
- **Every DB write that must cause an event goes through the outbox**, committed in the same transaction. There is no `db.Commit(); publisher.Send()` sequence anywhere.
- **Every consumer is idempotent** — either via a DB unique constraint, a Redis `SET ... NX`, or an explicit status-is-terminal check. This is required by the outbox's at-least-once semantics.
- **No silent message drops.** Every unhandleable message lands in a DLQ topic with enough provenance headers (original topic / partition / offset / reason / error) to manually replay. The only exception is transient infrastructure errors on `PaymentService`, where we rely on Kafka rebalance for retry — a future DLQ worker will add a retry budget there.

---

## 3. Domain Model

### Entities

**Event** (`internal/domain/event.go`)
```
ID, Name, TotalTickets, AvailableTickets, Version
Invariant: 0 <= AvailableTickets <= TotalTickets
Method: Deduct(quantity) (*Event, error) — immutable, returns a new
        *Event with decremented tickets; receiver is never mutated.
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
| EventRepository | Event CRUD + GetByIDForUpdate + DecrementTicket / IncrementTicket + Delete (for CreateEvent compensation) | PostgreSQL |
| OrderRepository | Order CRUD + status updates | PostgreSQL |
| OutboxRepository | Outbox CRUD + ListPending/MarkProcessed | PostgreSQL |
| InventoryRepository | Hot inventory deduction/reversion | Redis (Lua scripts) |
| OrderQueue | Async order stream (Enqueue/Dequeue/Ack) | Redis Streams |
| IdempotencyRepository | Request deduplication (24h TTL) | Redis |
| EventPublisher | Publish domain events | Kafka |
| PaymentService | Process payment events (returns `ErrInvalidPaymentEvent` on bad input so consumers can dead-letter) | Domain-layer service |
| PaymentGateway | Charge payments | Mock (configurable success rate) |
| DistributedLock | Leader election | PostgreSQL advisory locks |
| UnitOfWork | Transaction management | PostgreSQL |

`EventRepository.GetByID` performs a plain read; the explicit `GetByIDForUpdate` variant takes a `FOR UPDATE` row lock and MUST be called inside a UoW-managed transaction. The previously-deprecated `DeductInventory` method on `EventRepository` was removed in the remediation pass (no production callers).

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

### Migration History (7 files in `deploy/postgres/migrations/`)

| # | Purpose |
|---|---------|
| 000001 | Create `events` table + seed "Jay Chou Concert" (100 tickets) |
| 000002 | Create `orders` table |
| 000003 | Create `events_outbox` table |
| 000004 | Add `check_available_tickets_non_negative` + `UNIQUE(user_id, event_id)` |
| 000005 | Add `processed_at` column to `events_outbox` |
| 000006 | Replace unique constraint with partial index `WHERE status != 'failed'` — allows users to retry purchase after payment failure |
| 000007 | Add partial index `events_outbox_pending_idx ON events_outbox(id) WHERE processed_at IS NULL` — speeds up OutboxRelay.ListPending. Uses `CREATE INDEX CONCURRENTLY`; the file carries the `-- golang-migrate: no-transaction` pragma |

### Redis

| Key Pattern | Type | Purpose |
|-------------|------|---------|
| `event:{id}:qty` | String (integer) | Hot inventory counter (30d TTL so orphaned keys from deleted events eventually expire) |
| `orders:stream` | Stream | Async order queue |
| `orders:dlq` | Stream | Worker-side DLQ (messages that exhausted the 3-retry budget) |
| `idempotency:{key}` | String | Request deduplication (24h TTL) |
| `saga:reverted:{order_id}` | String | Compensation idempotency (7d TTL) |
| `saga:retry:p{partition}:o{offset}` | String (integer) | Durable saga-consumer retry counter (24h TTL) — survives restarts so `maxRetries=3` is really enforced |

### Kafka Topics

| Topic | Producer | Consumer Group | Consumer | Payload |
|-------|----------|----------------|----------|---------|
| `order.created` | OutboxRelay | `payment-service-group` (configurable via `KAFKA_PAYMENT_GROUP_ID`) | PaymentWorker (KafkaConsumer) | OrderCreatedEvent (id, user_id, event_id, quantity, amount) |
| `order.created.dlq` | KafkaConsumer on unparseable / `ErrInvalidPaymentEvent` | — | — (future DLQ worker) | Original payload + `x-original-{topic,partition,offset}` / `x-dlq-{reason,error}` headers |
| `order.failed` | PaymentService (via outbox) | `booking-saga-group` (configurable via `KAFKA_SAGA_GROUP_ID`) | SagaCompensator (via SagaConsumer) | OrderFailedEvent (order_id, event_id, user_id, quantity, reason) |
| `order.failed.dlq` | SagaConsumer after `sagaMaxRetries` | — | — (future DLQ worker) | Same provenance headers + reason=`max_retries` |

Group IDs and topic names are all sourced from `KafkaConfig` (`KAFKA_PAYMENT_GROUP_ID`, `KAFKA_ORDER_CREATED_TOPIC`, `KAFKA_SAGA_GROUP_ID`, `KAFKA_ORDER_FAILED_TOPIC`). The previous hardcoded `payment-service-group-test` literal was a latent prod/test bleed bug.

---

## 5. API Reference

### POST /api/v1/book
Book tickets for an event.
```json
// Request
{ "user_id": 123, "event_id": 1, "quantity": 1 }
// Headers: Idempotency-Key: <uuid> (optional, <= 128 chars)

// 200 OK
{ "message": "booking successful" }
// 409 Conflict
{ "error": "sold out" }
// 409 Conflict
{ "error": "user already bought ticket" }
// 500 Internal Server Error (sanitized)
{ "error": "internal server error" }
```

Error responses go through `api/errors.go :: mapError`, which matches sentinel errors via `errors.Is` and returns a safe public message. Raw DB / driver errors are logged server-side with correlation IDs but **never** echoed to the client.

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

> **Removed:** the legacy `POST /book` route (kept from Phase 0) was deleted in the remediation pass — it sat outside the `/api/v1` group and therefore bypassed the Nginx `location /api/` rate-limit zone. All callers must now use `/api/v1/book`.

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

**revert.lua** - Idempotent compensation (INCRBY-before-SET reordering)
```
1. EXISTS saga:reverted:{order_id} → if set, return 0 (already reverted)
2. INCRBY event:{id}:qty
3. SET saga:reverted:{order_id} NX EX 604800 (7d)
4. Return 1 (reverted)
```

Rationale for ordering (remediation item H6): under `appendfsync=always`
+ a mid-script Redis crash, the previous SETNX-then-INCRBY order could
persist the idempotency key WITHOUT the INCRBY, permanently skipping
inventory revert on all retries (silent under-revert). The new order
produces a loud over-revert instead (inventory > total, already
alerted on), which is much easier to catch and fix. Normal Lua
execution is atomic, so no concurrent callers can observe the
intermediate state.

### 6.2 Transactional Outbox

1. Worker writes `Order` + `OutboxEvent` in same PostgreSQL transaction
2. OutboxRelay (background goroutine) polls `events_outbox WHERE processed_at IS NULL` every 500ms
3. Publishes batch (up to 100) to Kafka
4. Marks each event as processed
5. Leader election via `pg_try_advisory_lock(1001)` - only 1 instance publishes

### 6.3 Saga Compensation

1. PaymentWorker consumes `order.created`, calls PaymentGateway.Charge()
2. On failure: updates order status to `failed`, inserts `order.failed` outbox event
3. SagaCompensator consumes `order.failed` (via `SagaConsumer`):
   - DB: `IncrementTicket` + update order status to `compensated` (same TX, idempotent via the `OrderStatusCompensated` guard)
   - Redis: `revert.lua` does an `INCRBY` then `SET NX EX 7d` — crash-safe ordering (see Section 6.1)
4. **Retry counter is Redis-backed** (`saga:retry:p{partition}:o{offset}` TTL 24h), so a consumer restart cannot reset it
5. After `sagaMaxRetries = 3` failures, the message is written to `order.failed.dlq` with provenance headers, the counter is cleared, the offset is committed, and both `saga_poison_messages_total` and `dlq_messages_total{topic="order.failed.dlq", reason="max_retries"}` counters are incremented — no more silent partition drops

Payment-side DLQ (`order.created.dlq`) works the same way: malformed JSON and `ErrInvalidPaymentEvent` from `PaymentService.ProcessOrder` are dead-lettered instead of being silently committed (which the old `return nil` branches did).

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
| `http_requests_total` | Counter | All requests by method/path/status (path is the Gin route template, bounded cardinality) |
| `http_request_duration_seconds` | Histogram | Request latency (p99-optimized buckets) |
| `bookings_total` | Counter | Booking outcomes (`success`, `sold_out`, `duplicate`, `error`) — pre-initialized at startup |
| `worker_orders_total` | Counter | Worker processing by outcome (`success`, `sold_out`, `duplicate`, `db_error`) — pre-initialized |
| `worker_processing_duration_seconds` | Histogram | Worker latency |
| `inventory_conflicts_total` | Counter | Redis-approved-but-DB-rejected oversells |
| `page_views_total` | Counter | Event page views (conversion funnel) |
| `dlq_messages_total` | Counter (`topic`, `reason`) | Messages written to a dead-letter topic. Pre-initialized labels cover `order.created.dlq` and `order.failed.dlq` for reasons `invalid_payload`, `invalid_event`, `max_retries` |
| `saga_poison_messages_total` | Counter | Saga events dead-lettered after exceeding `sagaMaxRetries` |
| `kafka_consumer_retry_total` | Counter (`topic`, `reason`) | Messages left UNCOMMITTED for Kafka rebalance retry because of a transient downstream error. Intentionally NOT dead-lettered (would cause overselling during DB hiccups). The `KafkaConsumerStuck` alert watches this — a sustained non-zero rate means a downstream dependency is degraded |

**Alerts (`deploy/prometheus/alerts.yml`):**

- `HighErrorRate` — HTTP 5xx ratio > 5% over 5m (2m `for` for hysteresis)
- `HighLatency` — p99 request duration > 2s
- `InventorySoldOut` — `increase(bookings_total{status="sold_out"}[5m]) > 0`. The previous `booking_sold_out_total` expression referenced a metric that did not exist in the code, so the alert was permanently silent until the remediation fix.
- `KafkaConsumerStuck` — `sum by (topic) (rate(kafka_consumer_retry_total[5m])) > 1` for 2m. Paired contract with the `kafka_consumer_retry_total` counter: when transient errors cause sustained rebalance retries, this alert fires so oncall investigates **downstream infra** (DB / Redis / payment gateway), NOT the consumer. The consumer is working as designed; the alert exists so "stuck but not dead" is operator-visible without having to dead-letter in-flight orders.

### Tracing (OpenTelemetry + Jaeger)
- Decorator pattern: `BookingServiceTracingDecorator`, `WorkerServiceMetricsDecorator`, `OutboxRelayTracingDecorator`
- `OutboxRelayTracingDecorator` now calls `span.RecordError` + `span.SetStatus(codes.Error)` on batch failures — the previous version always closed spans as OK
- `api/handler_tracing.go` uses a shared `recordHTTPResult(span, status)` helper that sets `span.status = Error` for **all** status >= 400, not just 5xx, so 4xx client errors show up in Jaeger search
- GRPC exporter to Jaeger (port 4317)
- **Sampler is configurable** via `OTEL_TRACES_SAMPLER_RATIO`: empty/1 → AlwaysSample (default), 0 → NeverSample, 0 < r < 1 → TraceIDRatioBased(r). Unparseable values log a warning and fall back to AlwaysSample (we never silently disable tracing)
- `initTracer` now **fails fast** (returns error to fx.Invoke) if either `resource.New` or `otlptracegrpc.New` fails, instead of letting a nil `traceExporter` crash the first span export

### Logging (Zap)
- Structured JSON to stdout
- Correlation ID injection via middleware
- Component-scoped loggers

### Dashboards (Grafana)
Pre-provisioned 6-panel dashboard: RPS, Latency Quantiles, Conversion Rate, IP Fairness, Saturation

### Profiling (pprof)
- `net/http/pprof` exposes `/debug/pprof/*` on a **separate** listener `:6060` — NOT on the main Gin router and NOT routed through nginx
- Controlled by `ENABLE_PPROF` env var (`true` to enable, defaults to `false`). Port 6060 is published in `docker-compose.yml` only for local use
- Wrapped in an `http.Server` with an fx `OnStop` hook (clean shutdown, no goroutine leak)
- Capture scripts: `scripts/pprof_capture.sh` grabs heap + allocs (30s sample) + goroutine profiles mid-test; `scripts/benchmark_gc.sh` orchestrates the whole run
- Use `go tool pprof -alloc_space -top pprof/heap.pb.gz` to see cumulative allocation hotspots

### Runtime tuning env vars

| Variable | Default (.env) | Fallback (compose) | Purpose |
|----------|----------------|--------------------|---------|
| `GOGC` | `400` | `100` | GC trigger ratio. Higher = GC less often, higher peak heap |
| `GOMEMLIMIT` | `256MiB` | (unset) | Soft memory limit. Pairs with GOGC so GC is aggressive only near the cap |
| `OTEL_TRACES_SAMPLER_RATIO` | `0.01` | `1` | Fraction of requests sampled. `0` disables, `1` always samples |
| `ENABLE_PPROF` | `true` | `false` | Whether to start the pprof listener on `:6060` |

---

## 8. Development Phases (Complete History)

| Phase | Date | Commit / PR | Description |
|-------|------|-------------|-------------|
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
| 13 | Apr 11 | PRs #7 / #8 / #9 / #12 / #13 | **Multi-agent review + remediation**: 66 findings across 6 review dimensions (domain/app, persistence, concurrency/cache, messaging/saga, api/payment, observability/deploy). All 6 CRITICAL and all 13 HIGH items fixed in [`fix/review-critical` (#8)](https://github.com/Leon180/booking_monitor/pull/8) and [`fix/review-high` (#9)](https://github.com/Leon180/booking_monitor/pull/9); 17 MEDIUM / 14 LOW / 6 NIT items fixed in [`fix/review-backlog` (#12)](https://github.com/Leon180/booking_monitor/pull/12); docs + `kafka_consumer_retry_total` metric + `KafkaConsumerStuck` alert + [`docs/reviews/SMOKE_TEST_PLAN.md`](reviews/SMOKE_TEST_PLAN.md) in [`fix/review-docs` (#13)](https://github.com/Leon180/booking_monitor/pull/13). Consolidated backlog lives at [`docs/reviews/ACTION_LIST.md`](reviews/ACTION_LIST.md). See the **Remediation highlights** block below for the user-visible changes. |
| 14 | Apr 12–13 | PRs #14 / #15 | **GC optimization**: baseline benchmark revealed a 70% RPS regression caused by fx.Decorate fix re-enabling tracing/metrics decorators + `AlwaysSample()` + per-request zap core clone. Fixed in two PRs. [`perf/gc-baseline` (#14)](https://github.com/Leon180/booking_monitor/pull/14) added the benchmark harness (pprof endpoint on `:6060`, `scripts/benchmark_gc.sh`, `scripts/gc_metrics.sh`, `scripts/pprof_capture.sh`) and three quick wins (`OTEL_TRACES_SAMPLER_RATIO=0.01`, `GOGC=400`, CorrelationIDMiddleware no longer clones the zap core) — RPS 7,984 → 20,552 (+157%). [`perf/gc-deep-fixes` (#15)](https://github.com/Leon180/booking_monitor/pull/15) followed with deep fixes: `sync.Pool` for Redis Lua script args, `strconv.Itoa` key concat (replaces `fmt.Sprintf` boxing), `GOMEMLIMIT=256MiB`, and a consolidated `CombinedMiddleware` that does exactly one `context.WithValue` + one `c.Request.WithContext` per request — mallocs/60s: 258M → 110M (−57%), GC cycles/60s: 202 → 86 (−57%). See the **Phase 14 highlights** block below. |

### Remediation highlights (Phase 13)

- **Kafka DLQ end-to-end**: new topics `order.created.dlq` / `order.failed.dlq`, new metrics `dlq_messages_total` / `saga_poison_messages_total`, Redis-backed saga retry counter, `ErrInvalidPaymentEvent` sentinel. No more silent message drops.
- **API safety**: `r.Run()` replaced with an explicit `http.Server{}` that honours `cfg.Server.ReadTimeout`/`WriteTimeout`; `api/errors.go :: mapError` sanitizes every error response so DB / driver errors never leak to clients; legacy `POST /book` route removed.
- **Secrets moved to `.env`**: all plaintext passwords (`postgres`, `grafana`, `redis`) now come from `${VAR}` substitution via a gitignored `.env` file with a tracked `.env.example`; docker-compose fails fast if values are missing.
- **`Config.Validate()`** rejects missing `DATABASE_URL` and (under `APP_ENV=production`) the localhost defaults on `REDIS_ADDR` / `KAFKA_BROKERS`.
- **Deploy hardening**: all six unpinned images now pinned (`golang:1.24-alpine`, `alpine:3.20`, `nginx:1.27-alpine`, `prom/prometheus:v2.54.1`, `grafana/grafana:11.2.2`, `jaegertracing/all-in-one:1.60`); Dockerfile runner stage runs as non-root `uid:10001`; Redis now has `--requirepass`.
- **Observability**: configurable OTel sampler via `OTEL_TRACES_SAMPLER_RATIO`, `recordHTTPResult` helper flags 4xx as span errors, `InventorySoldOut` alert now uses the real `bookings_total{status="sold_out"}` metric.
- **Persistence**: new partial index `events_outbox_pending_idx` (migration 000007), pool setters moved before the ping + new `ConnMaxLifetime`, `GetByID` split into plain + `GetByIDForUpdate`, 19 repository sites now wrap errors with `%w`.

### Phase 14 highlights (GC optimization)

- **Benchmark harness**: `net/http/pprof` on a separate `:6060` listener (gated by `ENABLE_PPROF=true`), `scripts/benchmark_gc.sh` / `scripts/gc_metrics.sh` / `scripts/pprof_capture.sh` orchestrate k6 + Go runtime metrics + heap/allocs profiles into a single report under `docs/benchmarks/`. The listener uses its own `http.Server` with an fx `OnStop` shutdown hook — no goroutine leak.
- **Sampler tuning**: `OTEL_TRACES_SAMPLER_RATIO` defaults to `0.01` (1%). Unsampled requests get a no-op span (zero allocation) instead of a full export through the batch span processor.
- **Runtime tuning**: `GOGC=400` + `GOMEMLIMIT=256MiB` — GC stays lazy during normal traffic but becomes aggressive as heap approaches the soft limit, preventing unbounded growth during spikes.
- **Hot-path allocation cuts**: `CombinedMiddleware` does exactly one `context.WithValue` + one `c.Request.WithContext` per request by injecting a scoped logger into the context (`internal/log/context.go`); Redis Lua script args reuse a `sync.Pool`-backed `[]interface{}`; inventory keys use `strconv.Itoa` concat instead of `fmt.Sprintf` to avoid interface boxing; sentinel errors (`errDeductScriptNotFound`, `errRevertScriptNotFound`, `errUnexpectedLuaResult`) replace per-call `fmt.Errorf`.
- **Result**: clean-run RPS 7,984 → 20,552 (+157%); allocations/60s 258M → 110M (−57%); GC cycles/60s 202 → 86 (−57%); GC pause max 79ms → 41ms (−48%); heap peak bounded by `GOMEMLIMIT` at ≤256MB.

---

## 9. Performance Benchmarks

| Configuration | RPS | P99 / P95 Latency | Notes |
|---------------|-----|-------------------|-------|
| Stage 1: Postgres only | ~4,000 | ~500ms (P99) | DB CPU bound (212%) |
| Stage 2: Redis hot inventory | ~11,000 | ~50ms (P99) | Memory / Network bound |
| Stage 2 + Kafka outbox | ~9,000 | ~100ms (P99) | Kafka throughput bound |
| Full system (pre-Phase 13) | ~26,879 | ~33ms (P95) | Feb 2026 baseline — tracing decorator silently disabled due to fx bug |
| Post-remediation, pre-GC | ~7,984 | ~98ms (P95) | Phase 13 fx fix re-enabled decorators + AlwaysSample → 70% regression |
| **Post-Phase 14 GC wins** | **~20,552** | **~45ms (P95)** | PR #14 quick wins (sampler 0.01 + GOGC=400 + no-clone middleware) |

Benchmark reports in `docs/benchmarks/` — see the `*_compare_c500` clean runs and the `*_gc_*` runs with pprof + GC metrics.

---

## 10. Remaining Roadmap

### High Priority
- **DLQ Worker**: A follow-up consumer that drains the four dead-letter destinations on a slower cadence and applies a retry-with-backoff policy before giving up. The DLQ **producers** (Worker `orders:dlq`, KafkaConsumer `order.created.dlq`, SagaConsumer `order.failed.dlq`) all exist and carry provenance headers (topic / partition / offset / reason / error); what's missing is the reader side plus a manual-replay CLI.

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
| `internal/infrastructure/api/errors.go` | `mapError(err) (status, publicMsg)` helper — sanitized public error responses |
| `internal/infrastructure/api/handler_tracing.go` | Tracing decorator + `recordHTTPResult` span helper |
| `internal/infrastructure/api/middleware.go` | `CombinedMiddleware` (Phase 14): single-pass logger + correlation ID injection (1 `context.WithValue` + 1 `c.Request.WithContext` per request) |
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

### Internal logging (`internal/log/`)
| File | Purpose |
|------|---------|
| `internal/log/log.go` | `Logger` type wrapping `*zap.Logger` + `AtomicLevel`; exposes `L()` (fast path), `S()` (sugar), `With()`, `Level()`, `Sync()` |
| `internal/log/options.go` | `Options` struct + `fillDefaults` (encoder, output, sampling). Decouples the package from `internal/config` |
| `internal/log/level.go` | `Level` type alias + `ParseLevel(string) (Level, error)` — typos fail startup, never silent fallback |
| `internal/log/context.go` | `NewContext` / `FromContext` — canonical ctx carry (klog/slog convention). `FromContext` returns `Nop` if unset, not a hidden global |
| `internal/log/nop.go` | `NewNop()` silent logger for tests and unwired paths |
| `internal/log/handler.go` | `LevelHandler()` — GET/POST `/admin/loglevel` for runtime level changes; mounted on the pprof listener |
| `internal/log/tag/tag.go` | Typed `zap.Field` constructors (`tag.OrderID`, `tag.Error`, etc.) — compile-time typo protection on the hot path |
| `internal/bootstrap/logmodule.go` | fx wiring: reads `cfg.App.LogLevel` → `log.ParseLevel` → `log.New`. Lives here (not in `internal/log/`) so the log package stays config-free |

### Configuration & Deployment
| File | Purpose |
|------|---------|
| `config/config.yml` | Application configuration |
| `.env.example` | Required-env template (Postgres / Redis / Kafka / Grafana / OTel). `.env` itself is gitignored |
| `docker-compose.yml` | Full stack orchestration (10 services); all secrets come from `${VAR}` substitution, fails fast on missing values |
| `Dockerfile` | Multi-stage build (`golang:1.24-alpine` → `alpine:3.20`), runner stage runs as non-root uid 10001 |
| `deploy/postgres/migrations/` | 7 SQL migration files |
| `deploy/redis/redis.conf` | Redis AOF persistence config (password is passed via `--requirepass ${REDIS_PASSWORD}` in docker-compose, not baked into the conf) |
| `deploy/nginx/nginx.conf` | Rate limiting + reverse proxy; bounded proxy timeouts + upstream keepalive |
| `deploy/prometheus/prometheus.yml` | Scrape config (15s interval) |
| `deploy/prometheus/alerts.yml` | Alert rules (`HighErrorRate`, `HighLatency`, `InventorySoldOut`, `KafkaConsumerStuck`) |
| `deploy/grafana/provisioning/` | Pre-configured datasources + dashboards (`timeseries` panels, `disableDeletion: true`) |

### Benchmark & Profiling Scripts
| File | Purpose |
|------|---------|
| `scripts/k6_comparison.js` | k6 load scenario (500k-ticket pool, 500 VUs, 60s constant-vus) used by both benchmark harnesses |
| `scripts/benchmark_compare.sh` | Two-run A/B benchmark producing historical-format comparison reports |
| `scripts/benchmark_gc.sh` | Phase 14: k6 + gc_metrics + pprof captured together; produces GC Runtime Metrics table |
| `scripts/gc_metrics.sh` | Polls `/metrics` every 5s for `go_gc_duration_seconds`, `go_memstats_*`, `go_goroutines` into CSV |
| `scripts/pprof_capture.sh` | Grabs heap + allocs (30s sample) + goroutine profiles from `:6060` mid-test |

### Documentation
| File | Purpose |
|------|---------|
| `docs/scaling_roadmap.md` | Stage 1-4 evolution plan |
| `docs/architecture/current_monolith.md` | Phase 7.7 Mermaid diagram |
| `docs/architecture/future_robust_monolith.md` | Phases 8-11 target architecture |
| `docs/adr/0001_async_queue_selection.md` | Redis Streams vs Kafka decision |
| `docs/reviews/phase2_review.md` | Redis integration review |
| `docs/reviews/ACTION_LIST.md` | Phase 13 consolidated remediation backlog (66 findings, severity-ranked, links back to review PRs) |
| `docs/reviews/SMOKE_TEST_PLAN.md` | 12-section repeatable runbook covering CRITICAL / HIGH remediation (pre-init metrics, legacy route removal, config validation, DLQ paths, etc.) |
| `docs/benchmarks/` | Timestamped performance reports; Phase 14 baseline + GC runs stored under `*_gc_*` and `*_compare_c500` prefixes |
