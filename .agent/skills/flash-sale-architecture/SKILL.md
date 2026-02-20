---
name: flash-sale-architecture
description: Step-by-step guide for implementing the production-grade flash sale booking system architecture. Use this skill when adding or extending any of the following components: Nginx/API Gateway, Redis Lua hot path, Redis Stream worker, Kafka event bus, Payment Service, or Email/Notification service. Covers the full flow from client request to order confirmation.
---

# Flash Sale Architecture

## System Overview

```
Client → Nginx (Rate Limit) → Ticket Service (Go)
                                     │
                    ┌────────────────┼────────────────┐
                    ▼                ▼                 ▼
              Redis Cluster      PostgreSQL          Kafka
              (Token Bucket      (Source of         (Event Bus)
               + Stream)          Truth)
                    │                │                 │
                    ▼                ▼                 ▼
              Go Worker         (orders table)   Payment Service
              (Consumer)                         Email / Analytics
```

## Request Flow

```
1. Client  → POST /book
2. Nginx   → Rate limit (e.g. 100 req/s per IP)
3. API     → Redis Lua script (atomic):
               a. Check token bucket (available_tickets > 0)
               b. DECRBY token
               c. XADD orders:stream (queue entry)
             → Return 202 Accepted immediately
4. Worker  → XREADGROUP orders:stream
             → DB Transaction:
               a. SELECT ... FOR UPDATE (inventory double-check)
               b. UPDATE events SET available_tickets = available_tickets - qty WHERE qty >= qty
               c. INSERT INTO orders (status='pending')
               d. INSERT INTO events_outbox (event_type='order_created', status='PENDING')
             → Commit
5. Outbox  → Relay: poll events_outbox → PUBLISH to Kafka topic `order.created`
6. Kafka   → Payment Service consumes `order.created`
             → Process payment
             → UPDATE orders SET status='paid'
             → Publish `order.paid` → Email / Analytics
```

## Architecture Diagrams

For a detailed visual understanding of the system, refer to:
1. [Current Monolith Architecture](../../docs/architecture/current_monolith.md) - Shows the successful integration of Phase 1-7 (Redis, Kafka, Payment).
2. [Future Robust Monolith](../../docs/architecture/future_robust_monolith.md) - Shows the target architecture after Phases 8-11 (Nginx, Sagas, Locks).

## Components

| Component | Status | Reference |
|---|---|---|
| Redis Lua (token bucket + stream) | ✅ Done | `internal/infrastructure/cache/lua/deduct.lua` |
| Go Worker (stream consumer) | ✅ Done | `internal/application/worker_service.go` |
| Outbox table | ✅ Done | `deploy/postgres/migrations/000003_add_outbox.up.sql` |
| Idempotency (API layer) | ✅ Done | `internal/infrastructure/cache/idempotency.go` |
| Kafka + Outbox Relay | ✅ Done | `internal/application/outbox_relay.go` |
| Payment Service | ✅ Done | `internal/application/payment/service.go` |
| Nginx / API Gateway | ❌ Pending | Phase 8. See [nginx.md](references/nginx.md) |
| Distributed Locks (Outbox Relay) | ❌ Pending | Phase 9. Prevent DB contention when scaling. |
| Saga Pattern (Payment Rollback) | ❌ Pending | Phase 10. Compensating transactions via Kafka. |
| Resilience (Redis MAXLEN & DLQ) | ❌ Pending | Phase 11. Protect Redis memory and handle toxic messages. |

## Implementation Road Map (Stabilizing the Monolith)

Instead of immediately splitting into microservices, we will scale horizontally by stabilizing the Modular Monolith:

1. **Phase 8: API Gateway (Nginx)** — Add Reverse Proxy to protect the naked Gin router with Connection Draining and Rate Limiting.
2. **Phase 9: Distributed Locking** — Use Redis to implement a Mutex so multiple `OutboxRelay` instances don't bombard the DB in a Race Condition.
3. **Phase 10: Saga Rollbacks** — If Payment fails, consume an `order.failed` Kafka event to `INCRBY` the inventory back into Redis so the ticket isn't lost permanently.
4. **Phase 11: Resilience & DLQ** — Guarantee Redis Stream stability via `MAXLEN` and automate Dead Letter Queue routing for failed worker jobs.

## Key Design Decisions

- **Redis is not the source of truth** — DB is. Redis only gates the hot path.
- **Outbox pattern** — Guarantees at-least-once delivery to Kafka without distributed transactions.
- **Worker idempotency** — DB `UNIQUE(user_id, event_id)` constraint prevents double orders even if the worker retries.
- **Monolith First** — The system is strictly decoupled internally via interfaces, allowing us to deploy as a monolith and avoid Kubernetes/Networking overhead, with the option to split later.
