# Benchmark Analysis: C500 Failure

**Date**: 2026-02-15
**Run**: `docs/benchmarks/20260215_004705_c500`

## 1. Bottleneck Identification
The benchmark for 500 VUs failed due to **Database CPU Saturation**.

-   **Metric**: `booking_db` CPU usage peaked at **212.18%**.
-   **Cause**: The PostgreSQL container is using >2 cores solely for managing row locks (`SELECT ... FOR UPDATE`).
-   **Resource Context**: The DB container had a limit of 4 CPUs (implied by host), but the contention on a single table (`events`) makes it effectively serial.

## 2. Latency Impact
-   **P95 Latency**: 788.82ms (Threshold: <200ms).
-   **Observation**: Requests are queuing at the database level, waiting for locks to be released. This confirms the "Convoy Effect" anticipated in the scaling roadmap.

## 3. Conclusion
The current architecture (Postgres-only) is physically limited by lock contention at ~300-400 Concurrent Users. To scale further, we must remove the locking mechanism from the hot path.

**Next Step**: Implement Redis for inventory management (Stage 2).
