# Benchmark Comparison Report

**Date**: Sat May  2 01:29:46 CST 2026
**Commit**: b6e564c — chore(ops): revert ticket-pool bump — keep flash-sale scale, fix methodology instead
**Parameters**: VUS=500, DURATION=60s

## Test Conditions (identical for both runs)

| Setting | Value |
| :--- | :--- |
| Script | `k6_comparison.js` |
| Ticket pool | 500,000 (realistic flash-sale scale; sells out partway through 60s) |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | 500 |
| Duration | 60s |

## Results

| Metric | Run A | Run B | Δ |
| :--- | ---: | ---: | :--- |
| **Throughput (req/s)** | 50084.309003/s | 45749.058395/s | — |
| **p95 latency** | 15.91ms | 14.01ms | — |
| **avg latency** | 5.88ms | 5.27ms | — |
| **Booking accepted** | N/A | N/A | — |
| **Business errors** | 0.00% | 0.00% | — |
| **Total iterations** | 3005577 | 2745425 | — |

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
