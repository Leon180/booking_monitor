# Benchmark Comparison Report — CP8: Header-Bearing N4 Cost (Idempotency-Key)

**Date**: Fri May 1 17:56–18:00 CST 2026
**Commit**: `48aedae` — `refactor(observability): CP3b — split metrics.go into per-concern files, single central init() (#58)`
**Run scope**: 4 runs total — 2× baseline (`IDEMPOTENCY=false`) + 2× header-bearing (`IDEMPOTENCY=true`). All runs same docker host state, same minute window. Calibrates the cost of the **N4 idempotency-key fingerprint path** (PR #48) which prior baselines never exercised because `k6_comparison.js` did not send the `Idempotency-Key` header.

**Parameters**: VUS=500, DURATION=60s, ticket pool 500,000, target `http://app:8080/api/v1` (direct, bypasses nginx rate limit).

## Why this benchmark

PR #48 (N4) introduced Stripe-style idempotency-key fingerprinting: every request bearing `Idempotency-Key` is hashed (canonical body + user_id + route → SHA-256), stored alongside the cached response, and replayed on collision (or returns 409 on body mismatch). Prior baselines (PR #37 → CP3b / PR #58) never sent the header, so the fingerprint compute + Redis SETNX + value-store path was completely unmeasured. The CP3b no-regression report ([../20260501_174117_compare_c500/comparison.md](../20260501_174117_compare_c500/comparison.md)) flagged this gap explicitly:

> "the next benchmark to plan is **CP8 — header-bearing N4 baseline** … until that lands, the N4 cost estimate (2–5 % p95 / <2 % RPS) remains uncalibrated."

This run is that calibration.

## What changed in instrumentation

`scripts/k6_comparison.js` (and the `benchmark_compare.sh` wrapper) gained an `IDEMPOTENCY` env flag. When set to `true`, each iteration mints a unique key:

```javascript
if (__ENV.IDEMPOTENCY === 'true') {
    headers['Idempotency-Key'] = `bench-${__VU}-${__ITER}-${Date.now()}`;
}
```

Per-iteration uniqueness deliberately exercises the **cold path** (SETNX wins, fingerprint is computed and written, payload is stored). The replay branch (same key reused) is a separate measurement and not in scope here.

## Results

### Baseline — no `Idempotency-Key` header (`IDEMPOTENCY=false`)

Source: [`docs/benchmarks/20260501_175712_compare_c500/`](../20260501_175712_compare_c500/)

| Metric | Run A | Run B | Avg |
| :--- | ---: | ---: | ---: |
| Throughput (req/s) | 50,458 | 47,373 | **48,915** |
| p95 latency | 15.83 ms | 13.78 ms | **14.81 ms** |
| avg latency | 5.94 ms | 5.24 ms | 5.59 ms |
| Total iterations | 3,027,852 | 2,842,607 | — |
| Business errors | 0.00 % | 0.00 % | — |

### Header-bearing — unique `Idempotency-Key` per iteration (`IDEMPOTENCY=true`)

Raw outputs in this directory.

| Metric | Run A | Run B | Avg |
| :--- | ---: | ---: | ---: |
| Throughput (req/s) | 40,200 | 39,679 | **39,940** |
| p95 latency | 21.43 ms | 21.77 ms | **21.60 ms** |
| avg latency | 10.58 ms | 10.65 ms | 10.62 ms |
| Total iterations | 2,412,424 | 2,381,319 | — |
| Business errors | 0.00 % | 0.00 % | — |

### Delta — measured cost of the N4 fingerprint path

| Metric | Baseline avg | Header-bearing avg | Δ |
| :--- | ---: | ---: | ---: |
| Throughput | 48,915 RPS | 39,940 RPS | **−18.4 %** |
| p95 latency | 14.81 ms | 21.60 ms | **+45.9 %** |
| avg latency | 5.59 ms | 10.62 ms | **+90 %** |

## Interpretation — N4 cost is real and measurable

The pre-N4 estimate ("2–5 % p95 / <2 % RPS") was significantly low. Actual cost on this docker-on-laptop setup at 500 VUs is **≈18 % RPS / ≈46 % p95 / ≈90 % avg latency**.

What the cold path does on every header-bearing request:

1. **Redis SETNX** of the idempotency key (network round-trip, single-key write)
2. **SHA-256 fingerprint** computation over canonical(body) + user_id + route
3. **Redis value-write** of the response payload + fingerprint (separate round-trip after handler returns)
4. Key normalisation + length validation at the Gin middleware layer

Two Redis round-trips that the baseline path doesn't pay are the dominant share. The fingerprint hash itself is sub-microsecond at request scale (SHA-256 on a ≈50-byte canonical body) — the latency budget is in I/O, not CPU.

## Why the cost is acceptable for the contract it provides

N4 is a **correctness control**, not a performance feature. The two protections it adds:

- **Same key, same body → cached replay** (fast, no double-charge)
- **Same key, different body → 409 Conflict** (refuses the lost-update class of bug)

Without these, a retry storm where a client mutates the body between attempts would silently coalesce into "the first version wins, the rest are ignored." The Stripe blog calls this out as the most-overlooked failure mode in idempotency-key APIs. The cost paid here is the price of refusing that failure mode — the alternative is data inconsistency under retry, which is unacceptable at any throughput.

For routes that don't carry the header, the path is unchanged — no penalty for non-idempotent or unauthenticated traffic. The `/api/v1/book` handler currently makes the header optional; clients opt in to the contract.

## What this benchmark does NOT measure

- **Replay branch cost** (same key reused) — fingerprint compare + cache return; expected lower than cold path. Future variant: set a stable per-VU key.
- **Mismatch branch cost** (same key, different body → 409) — expected similar to replay branch (compare + return).
- **Production-shape Redis latency** — a single-host docker `redis:7-alpine` round-trip is ~0.3 ms; production-grade Redis Sentinel + replication may differ.
- **Header validation failures** — keys longer than the configured cap take a fast 400 path, not the SETNX path.

## Operational implications

- **Capacity planning**: SLO sizing for traffic that consistently uses idempotency keys must use the ~40k RPS baseline, not the 48k RPS no-header baseline.
- **Redis tier sizing**: each idempotent request is two extra Redis ops; provision read/write capacity accordingly.
- **Future optimisation candidates** (do NOT implement without an SLO ask):
  - Pipeline the SETNX + value-write into a single Lua script (one round-trip)
  - Move fingerprint compute off the request hot path via `sync.Pool`-cached SHA-256 hashers
  - Treat the value-write as fire-and-forget under a write-behind queue (loses durability — only worth it for non-financial endpoints)

None are justified yet — N4 throughput sits comfortably above current production-shaped traffic targets.

## Caveats

- k6-on-Docker on a developer laptop. Absolute numbers don't generalise.
- The 500k ticket pool depletes within the 60-s window in both arms (sells out partway through). After depletion, every subsequent request takes the 409 sold-out fast path — but **even the 409 path bears the N4 cost** when the header is set, because middleware runs before the handler. So the comparison is fair: same workload, same fast/slow split, only the header differs.
- The Apr 28 A4 baseline (54–57k RPS) was a one-time outlier; this run uses the more representative pre-A4 historical band as ground truth (49–51k RPS, matched by the 48,915 baseline above).

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt) — header-bearing Run A
- [run_b_raw.txt](run_b_raw.txt) — header-bearing Run B
- [`../20260501_175712_compare_c500/run_a_raw.txt`](../20260501_175712_compare_c500/run_a_raw.txt) — baseline Run A
- [`../20260501_175712_compare_c500/run_b_raw.txt`](../20260501_175712_compare_c500/run_b_raw.txt) — baseline Run B
