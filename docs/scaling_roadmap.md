# System Scaling Roadmap

This document outlines the evolution of the Booking System to handle high-concurrency "Flash Sale" scenarios (100k+ users).

## Stage 1: The "Acid" Truth (Current)
**Architecture**: API -> Postgres (`SELECT ... FOR UPDATE`)
- **Mechanism**: Pessimistic locking on the database inventory row.
- **Pros**: 100% Data Consistency. No overselling. Simple to implement.
- **Cons**: Database Connection/CPU Bottleneck. Requests queue up at the DB. High latency under load.
- **Limit**: ~2k - 5k req/sec (depending on DB hardware).
- **Status**: ✅ Implemented.

## Stage 2: The "Fast" Cache (Redis)
**Architecture**: API -> Redis (Lua Script) -> Background Sync -> Postgres
- **Mechanism**: Move inventory counter to Redis. Use Atomic DECR or Lua scripts to check-and-decrement.
- **Pros**: Extremely fast (In-memory). Handles 50k+ req/sec easily.
- **Cons**: Complexity of consistency. What if Redis crashes before syncing to DB? (Write-behind or Write-through patterns).
- **Goal**: Offload "read/check" pressure from the main DB.

## Stage 3: The "Spike" Absorber (Kafka/Queue)
**Architecture**: API -> Kafka Producer -> (Ack "Pending") ... -> Consumer Group -> Postgres
- **Mechanism**: API accepts request immediately, pushes to Queue. Worker reads queue and processes booking.
- **Pros**: Infinite buffering. User gets instant feedback ("Processing..."). System survives 1M users/sec spikes by smoothing the load.
- **Cons**: Asynchronous. User doesn't know *immediately* if they got the ticket. Requires polling or websocket for status updates.

## Stage 4: The Distributed Future (Sharding - Optional)
**Architecture**: Sharded DB / Geo-distributed Events.
- **Mechanism**: Split events across different DB instances.

## The "Real World" Check: Capacity Planning
Since we cannot easily simulate **1 Million Users** on a laptop, we use **Unit Capacity Math**:

1.  **Measure**: Find the max throughput of **1 App Instance + 1 DB Instance**.
    -   *Current (Postgres)*: ~4,000 req/sec (per `make stress-k6`).
2.  **Calculate Scale**:
    -   Goal: 1,000,000 req/sec.
    -   Required Instances: `1,000,000 / 4,000 = 250 App Instances`.
3.  **Optimize Unit**:
    -   If we switch to **Redis**, we might get ~50,000 req/sec per instance.
    -   New Requirement: `1,000,000 / 50,000 = 20 App Instances`.

**Conclusion**: We don't need 1M users to know if the system scales. We just need to maximize the **requests per second per node**.

---

## Proposed Next Step: Stage 2 (Redis)
We will implement the **Redis Token Bucket** or **Inventory Counter** pattern.
1. `docker-compose` add Redis.
2. Update `EventRepository` to use Redis for hot inventory.
3. Compare performance benchmarks (Stage 1 vs Stage 2).
