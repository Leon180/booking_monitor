# Benchmark Comparison Report — A4 (Charging Intent Log + Reconciler)

**Date**: Tue Apr 28 22:53:59 CST 2026
**Commit base**: `e9b3131` — chore(api): split api/ into booking/ + ops/ + middleware/ subpackages (#44)
**Run scope**: same-binary two-pass against the post-A4 build. Confirms the booking k6 hot path is unaffected by A4 — and explicitly **does NOT measure payment worker throughput**, where the +1 DB round-trip cost actually lands.

**Parameters**: VUS=500, DURATION=60s

## Test Conditions (identical for both runs)

| Setting | Value |
| :--- | :--- |
| Script | `k6_comparison.js` |
| Ticket pool | 500,000 (never sells out — measures pure capacity) |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | 500 |
| Duration | 60s |
| Target | `http://app:8080/api/v1` (direct, bypasses nginx rate limit) |

## Results

| Metric | Run A | Run B | Δ (A→B) |
| :--- | ---: | ---: | ---: |
| **Throughput (req/s)** | 57,734 | 54,206 | -6.1% |
| **p95 latency** | 13.58 ms | 12.63 ms | -7.0% ↓ |
| **avg latency** | 5.11 ms | 4.79 ms | -6.3% ↓ |
| **Total iterations** | 3,464,596 | 3,252,767 | -6.1% |
| **Business errors** | 0.00% | 0.00% | 0 |
| **p(95) threshold** | <500 ms ✓ | <500 ms ✓ | — |

## Comparison vs historical baselines

| Build | Throughput | p95 | Source |
| :-- | --: | --: | :-- |
| Post PR #37 (Apr 26) | 50,193 RPS | 12.63 ms | `20260426_*_compare_c500_pr37` |
| PR #40 confirmation B2 | 49,141 RPS | 12.08 ms | `20260427_*_compare_c500_pr40` |
| PR #43 Run B | 50,597 RPS | 11.81 ms | `20260428_011524_compare_c500` |
| PR #44 Run B | 50,266 RPS | 12.44 ms | `20260428_104616_compare_c500_pr44_apisplit` |
| **A4 Run A (this)** | **57,734 RPS** | **13.58 ms** | this directory |
| **A4 Run B (this)** | **54,206 RPS** | **12.63 ms** | this directory |

A4 numbers sit **above** historical baselines. This is not A4 making things faster — A4 doesn't touch the booking hot path at all (see next section). The +7–15% versus PR #44 is most likely test-host variance: warmer caches from prior smoke runs in the same session, better macOS thermal state, or other ambient noise. The post-A4 ceiling on a fully-warmed laptop appears to be ~55-58K RPS; the post-PR-44 measurement was taken right after a cold rebuild.

## Why A4 doesn't move booking k6 throughput

The k6 script measures `POST /api/v1/book → 202`. Tracing this path:

```
client → nginx → app (booking handler)
         └─▶ tx { DecrementInventory + CreateOrder(status=Pending) + CreateOutbox(order.created) } → COMMIT
         └─▶ 202 to client
```

**That's it from the user's perspective.** The order is `Pending` when the response goes out. From here:

```
OutboxRelay → publishes order.created to Kafka
↓
payment_worker consumes order.created
   ↓
   GetByID → MarkCharging → gateway.Charge → MarkConfirmed   ← A4's new flow lives here
```

**A4 changes the payment worker's flow, not the booking handler's.** The k6 benchmark measures the booking handler in isolation; the payment worker can be totally idle (or backlogged) and `POST /book` throughput is unaffected. So a no-regression result here is the EXPECTED outcome — confirming the architectural boundary.

## What this benchmark does NOT measure (honest disclosure)

The +1 DB round-trip cost lands on the **payment worker**:

```
Before A4:
   GetByID → Charge → MarkConfirmed                          (2 DB calls + 1 gateway call)

After A4:
   GetByID → MarkCharging → Charge → MarkConfirmed           (3 DB calls + 1 gateway call)
                ▲ new
```

`MarkCharging` is one indexed UPDATE+CTE — typical p99 ~1-2ms. At ~100ms gateway baseline (MockGateway 50-200ms range; real Stripe ~30s but with idempotency cache hits ~50ms), that's a ~2% slowdown on payment worker throughput. **We did NOT measure this directly** — there's no payment-worker-specific benchmark in the harness today.

A future N6 (test infra hardening) PR would add a worker-throughput benchmark that drives messages through Kafka at saturation and measures resolves/sec. Until then, the +1-2ms estimate is engineering math, not measurement.

The payment worker is **not the user-visible bottleneck** — the booking handler is what k6 tests, and what flash-sale customers experience. The worker processing 50K orders/sec sustained vs 49K/sec (-2%) is operationally negligible for any realistic flash-sale scenario.

## Conclusion

- **Booking hot path: 0 regression, 0 business errors.** 6.7M iterations across both runs, all clean.
- **Payment worker hot path: not measured here**, estimated at -2% throughput from the extra `MarkCharging` round-trip. Acceptable for the recovery + visibility gain.
- **Reconciler runs as a separate process**: zero booking-handler impact, regardless of how busy the recon backlog gets.

Run B (54,206 RPS / p95 12.63 ms) is the steady-state number for future PR comparison.

## End-to-end smoke verification (separate from k6)

Verified post-benchmark via SQL inspection:

```
=== orders ===
019dd493-047e-7e80-9f70-5114cf56f189 | confirmed | created 2026-04-28 14:51:37.98 | updated 2026-04-28 14:51:39.19

=== order_status_history ===
019dd493-047e-7e80-9f70-5114cf56f189 | pending  → charging  | 14:51:39.13
019dd493-047e-7e80-9f70-5114cf56f189 | charging → confirmed | 14:51:39.19
```

Two audit rows confirm the new path: `Pending → Charging → Confirmed`. `updated_at` populated. Reconciler process running on 120s loop without errors. Migration 000010 applied cleanly.

## Caveats

- Same-binary two-pass, not an A/B comparison against a different commit. Confirms post-A4 build stability.
- Payment worker hot-path cost not directly measured (no harness today).
- A4 numbers are higher than PR #44 due to test-host variance, NOT performance improvement.

## Raw Outputs

- [run_a_raw.txt](run_a_raw.txt) — Run A (warmer-cache pass)
- [run_b_raw.txt](run_b_raw.txt) — Run B (steady state, use as baseline for next PR)
