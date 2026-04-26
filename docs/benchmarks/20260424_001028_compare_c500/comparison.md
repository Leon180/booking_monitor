# Benchmark Comparison Report

**Date**: Fri Apr 24 00:12:35 CST 2026
**Commit**: 76904c5 — refactor(log): Temporal-style internal/log package + dynamic level
**Parameters**: VUS=500, DURATION=60s

## Test Conditions (identical for both runs)

| Setting | Value |
| :--- | :--- |
| Script | `k6_comparison.js` |
| Ticket pool | 500,000 (never sells out) |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | 500 |
| Duration | 60s |

## Results

| Metric | Run A | Run B | Δ |
| :--- | ---: | ---: | :--- |
| **Throughput (req/s)** | 52281.977665/s | 47695.165172/s | — |
| **p95 latency** | 14.77ms | 13.13ms | — |
| **avg latency** | 5.53ms | 5ms | — |
| **Booking accepted** | N/A | N/A | — |
| **Business errors** | 0.00% | 0.00% | — |
| **Total iterations** | 3138196 | 2862245 | — |

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
