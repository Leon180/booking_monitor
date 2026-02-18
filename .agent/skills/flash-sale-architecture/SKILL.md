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

## Components

| Component | Status | Reference |
|---|---|---|
| Redis Lua (token bucket + stream) | ✅ Done | `internal/infrastructure/cache/lua/deduct.lua` |
| Go Worker (stream consumer) | ✅ Done | `internal/application/worker_service.go` |
| Outbox table | ✅ Done | `deploy/postgres/migrations/000003_add_outbox.up.sql` |
| Idempotency (API layer) | ✅ Done | `internal/infrastructure/cache/idempotency.go` |
| Nginx / API Gateway | ❌ Pending | See [nginx.md](references/nginx.md) |
| Kafka + Outbox Relay | ❌ Pending | See [kafka.md](references/kafka.md) |
| Payment Service | ❌ Pending | See [payment.md](references/payment.md) |

## Implementation Order

When adding new components, follow this order (each depends on the previous):

1. **Kafka** — Add to `docker-compose.yml`, implement outbox relay worker
2. **Payment Service** — New Go microservice consuming `order.created`
3. **Nginx** — Add as reverse proxy with rate limiting in `docker-compose.yml`
4. **Email/Notify** — Kafka consumer for `order.paid`

## Key Design Decisions

- **Redis is not the source of truth** — DB is. Redis only gates the hot path.
- **Outbox pattern** — Guarantees at-least-once delivery to Kafka without distributed transactions.
- **Worker idempotency** — DB `UNIQUE(user_id, event_id)` constraint prevents double orders even if the worker retries.
- **Order status lifecycle**: `pending` → `paid` → (optionally) `cancelled`
