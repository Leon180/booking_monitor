# Benchmark Comparison Report — CP8 Baseline Arm (no Idempotency-Key)

**Date**: Fri May  1 17:59:19 CST 2026
**Commit**: 48aedae — refactor(observability): CP3b — split metrics.go into per-concern files, single central init() (#58)
**Parameters**: VUS=500, DURATION=60s, IDEMPOTENCY=false

> This is the **baseline arm** for CP8. The full side-by-side analysis lives in the header-bearing dir: [`../20260501_175422_compare_c500/comparison.md`](../20260501_175422_compare_c500/comparison.md). Standalone summary preserved below for raw-data integrity.

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
| **Throughput (req/s)** | 50457.83785/s | 47372.536669/s | — |
| **p95 latency** | 15.83ms | 13.78ms | — |
| **avg latency** | 5.94ms | 5.24ms | — |
| **Booking accepted** | N/A | N/A | — |
| **Business errors** | 0.00% | 0.00% | — |
| **Total iterations** | 3027852 | 2842607 | — |

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
