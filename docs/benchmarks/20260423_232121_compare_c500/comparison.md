# Benchmark Comparison Report

**Date**: Thu Apr 23 23:23:28 CST 2026
**Commit**: 337c2be — docs: sync bilingual docs to Phase 14 + fix CLAUDE.md drift (#16)
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
| **Throughput (req/s)** | 56668.85617/s | 53983.864369/s | — |
| **p95 latency** | 10.31ms | 10.5ms | — |
| **avg latency** | 3.97ms | 4.08ms | — |
| **Booking accepted** | N/A | N/A | — |
| **Business errors** | 0.00% | 0.00% | — |
| **Total iterations** | 3400917 | 3239493 | — |

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
