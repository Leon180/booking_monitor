# Repeatability Check Report

**Date**: 2026-05-06 (Asia/Taipei)  
**Goal**: verify that the Lua runtime metadata optimization is a stable gain, not a one-run fluke  
**Conditions**: `scripts/k6_comparison.js`, 500 VUs, 60s, direct `app:8080`, `IDEMPOTENCY=false`

## Valid sample set

The machine-load safety stop interrupted the fourth pair, so this report uses the **three complete main runs** and **three complete branch runs** below:

| Sample | Main raw output | Branch raw output |
| :--- | :--- | :--- |
| Pair 1 | [run_a_raw.txt](../20260505_235455_compare_c500_lua_runtime_meta/run_a_raw.txt) | [run_b_raw.txt](../20260505_235455_compare_c500_lua_runtime_meta/run_b_raw.txt) |
| Pair 2 | [main_run_1_raw.txt](main_run_1_raw.txt) | [branch_run_1_raw.txt](branch_run_1_raw.txt) |
| Pair 3 | [main_run_2_raw.txt](main_run_2_raw.txt) | [branch_run_2_raw.txt](branch_run_2_raw.txt) |

Discarded during the run:

- an interrupted `main` third-pass sample, excluded from the report because it did not complete under the same conditions

## Per-run results

| Pair | Variant | HTTP req/s | Accepted bookings/s | p95 |
| :--- | :--- | ---: | ---: | ---: |
| 1 | main | 39,258.698877 | 8,332.256727 | 22.32ms |
| 1 | branch | 44,491.373953 | 8,330.909046 | 15.71ms |
| 2 | main | 40,801.596385 | 8,331.610524 | 21.50ms |
| 2 | branch | 47,388.809332 | 8,332.090104 | 16.22ms |
| 3 | main | 39,374.486294 | 8,331.385995 | 22.22ms |
| 3 | branch | 43,103.694489 | 8,330.169565 | 15.53ms |

## Averages and spread

| Metric | Main avg | Branch avg | Delta |
| :--- | ---: | ---: | ---: |
| HTTP req/s | 39,811.593852 | 44,994.625925 | **+13.02%** |
| Accepted bookings/s | 8,331.751082 | 8,331.056238 | **-0.008%** |
| p95 | 22.01ms | 15.82ms | **-28.13%** |

Ranges:

- Main HTTP req/s: `39,258.698877 .. 40,801.596385`
- Branch HTTP req/s: `43,103.694489 .. 47,388.809332`
- Main p95: `21.50ms .. 22.32ms`
- Branch p95: `15.53ms .. 16.22ms`
- Main accepted bookings/s: `8,331.385995 .. 8,332.256727`
- Branch accepted bookings/s: `8,330.169565 .. 8,332.090104`

## Interpretation

The signal is stable:

- Branch throughput beats main in **all three valid samples**
- Branch p95 is lower in **all three valid samples**
- Accepted bookings/s stays effectively flat, so the win is not coming from weaker booking correctness or lower accepted volume

This is the expected performance shape for the optimization:

- the sold-out / reject-heavy HTTP path gets cheaper
- the accepted reservation ceiling stays bound by downstream async work, not by an extra hot-path lookup

## Conclusion

The Lua runtime metadata optimization is **repeatably effective** on this machine under the standard 500 VU / 60s comparison setup:

- total HTTP throughput improves by about **13%**
- p95 improves by about **28%**
- accepted bookings/s is effectively unchanged

That is strong enough evidence to treat the optimization as real, even without finishing the fourth pair.
