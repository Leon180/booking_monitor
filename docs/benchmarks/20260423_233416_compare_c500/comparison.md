# Benchmark Comparison Report

**Date**: Thu Apr 23 23:36:23 CST 2026
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
| **Throughput (req/s)** | 51327.370241/s | 49519.343036/s | — |
| **p95 latency** | 11.97ms | 13.04ms | — |
| **avg latency** | 4.56ms | 4.92ms | — |
| **Booking accepted** | N/A | N/A | — |
| **Business errors** | 0.00% | 0.00% | — |
| **Total iterations** | 3079955 | 2971625 | — |

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
