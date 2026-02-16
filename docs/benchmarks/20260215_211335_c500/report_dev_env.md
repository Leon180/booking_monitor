# Benchmark Comparison: Dev vs Prod Environment

## Overview
This report compares the performance of the system running in **Development Mode** (Debug logs, GIN_MODE=debug) versus the previous **Production Mode** (Info logs, GIN_MODE=release).

- **Prod Run (Release)**: `20260215_210328_c500`
- **Dev Run (Debug)**: `20260215_211335_c500`

## Key Metrics Comparison

| Metric | Prod (Release) | Dev (Debug) | Difference |
| :--- | :--- | :--- | :--- |
| **Throughput (RPS)** | ~21,793 req/s | ~16,592 req/s | **-23.8%** |
| **Latency P95** | 42 ms | 85.29 ms | **+103% (2x slower)** |
| **Latency P90** | 29.78 ms | 60.14 ms | **+102%** |
| **Failed Requests** | 0 (Business) | 0 (Business) | - |
| **VUs** | 500 | 500 | - |

## Analysis
Running in "Dev Env" (Debug mode) introduced noticeable overhead:
1.  **Latency Doubled**: P95 latency increased from 42ms to 85ms. This is expected due to request logging and runtime checks in Gin's debug mode.
2.  **Throughput Drop**: Throughput decreased by ~24%, from 21.7k to 16.6k RPS.
3.  **Still Performant**: Despite the debug overhead, the system still comfortably meets the <200ms latency requirement, demonstrating the efficiency of the underlying architecture (Redis + Go).

## Conclusion
While Development Mode is significantly slower (2x latency), it is still performant enough for functional testing and debugging. However, for maximum load capacity, **Production Mode** is essential.
