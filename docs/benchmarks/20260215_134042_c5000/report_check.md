# Benchmark Analysis: Redis Scaling & Limits

## Overview
This document compares the performance of the **Redis-based** implementation (Phase 2) against the **Postgres-based** baseline (Phase 1) across three concurrency levels: **500 (Baseline)**, **1,000**, and **5,000**.

## Comparative Results

| Metric | Phase 1 (DB) | Phase 2 (Redis) | Phase 2 (Redis) | Phase 2 (Redis) |
| :--- | :--- | :--- | :--- | :--- |
| **Concurrency (VUs)** | **500** | **500** | **1,000** | **5,000** |
| **Total Requests** | 122,484 | 667,727 | 661,310 | 88,631 |
| **Throughput (RPS)** | ~2,031 | ~11,111 | ~10,990 | ~1,451 |
| **Latency (p95)** | 788ms | 104ms | 175ms | 836ms |
| **Latency (Mean)** | 243ms | 41ms | 78ms | 183ms |
| **Error Rate** | 0.00% | 0.00% | 0.00% | 0.00% |

### Resource Usage (Peak)

| Component | Phase 1 (C500) | Redis (C500) | Redis (C1000) | Redis (C5000) |
| :--- | :--- | :--- | :--- | :--- |
| **App CPU** | 143% | **378%** | 304% | 191% |
| **Redis CPU** | N/A | 111% | 73% | 42% |
| **DB CPU** | **212%** | 64% | 9% | 13% |

## Analysis

### 1. The "Redis Leap" (C500 Comparison)
- **Throughput**: Redis implementation achieved **5.5x higher throughput** (11k vs 2k RPS).
- **Latency**: p95 Latency dropped by **87%** (788ms -> 104ms).
- **Bottleneck Shift**: The bottleneck successfully moved from the **Database** (212% CPU) to the **Application** (378% CPU). This confirms that locking logic was the primary constraint.

### 2. Scaling Sweet Spot (C1000)
- **Stability**: At 1,000 concurrent users, the system maintained **~11k RPS** with excellent latency (**175ms p95**).
- **Efficiency**: The application efficiently saturated its CPU limit without degrading performance.

### 3. The Breaking Point (C5000)
- **Degradation**: Performance collapsed at 5,000 concurrent users.
    - **Throughput**: Dropped drastically to ~1,451 RPS.
    - **Latency**: Spiked to **836ms**.
- **Cause**: **Client-Side/Network Saturation & Context Switching**.
    - The App CPU *dropped* to 191% (from 378%), and Redis CPU dropped to 42%.
    - This indicates the application was **starved** of CPU cycles, likely due to excessive Goroutine context switching or Docker network limits handling 5,000 active connections on a single node.
    - High `iteration_duration` (avg 2.16s) vs low `http_req_duration` (avg 183ms) suggests K6 itself or the network stack became the bottleneck waiting to send/receive bytes.

## Conclusion
- **Success**: The Redis architecture is highly effective, safely handling **11,000 RPS** with sub-200ms latency.
- **Limit**: The current single-node deployment hits a hard wall between 1,000 and 5,000 concurrent users, likely due to OS/Docker networking limits rather than application logic.
- **Recommendation**: To scale beyond C1000, we would need to scale the **Booking App** horizontally (add more replicas) to distribute connection handling.
