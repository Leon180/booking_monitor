# Benchmark Comparison Report

**Date**: Thu Apr 23 23:15:27 CST 2026
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
| **Throughput (req/s)** | 52599.309028/s | 47776.74694/s | — |
| **p95 latency** | 14.23ms | 13.76ms | — |
| **avg latency** | 5.2ms | 5.15ms | — |
| **Booking accepted** | N/A | N/A | — |
| **Business errors** | 0.00% | 0.00% | — |
| **Total iterations** | 3156265 | 2867067 | — |

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
