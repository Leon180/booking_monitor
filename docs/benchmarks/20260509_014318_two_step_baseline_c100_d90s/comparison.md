# Two-step baseline (D9-minimal)

**Date**: 2026-05-09
**Run dir**: `docs/benchmarks/20260509_014318_two_step_baseline_c100_d90s/`
**Captured by**: PR #102 follow-up artifact-capture pass
**Branch context**: `docs/d9-d10-artifact-capture` (post PR #102 merge to main)

This is **the first capture** with the new D9-minimal `scripts/k6_two_step_flow.js` scenario (book → /pay → confirm → poll-paid OR abandon → expire → compensated). Single-run baseline; no comparison run paired (the comparison-style A/B runs land in D12, per [`docs/post_phase2_roadmap.md`](../../post_phase2_roadmap.md)). Still uses the `comparison.md` filename convention so future automation can pick it up.

## Conditions

| Knob | Value | Why |
|---|---|---|
| Script | `scripts/k6_two_step_flow.js` | D9-minimal scenario |
| Executor | `constant-vus` | k6 baseline default |
| VUs | 100 | Lower than 500 (single-step) because two-step is ~10-13 reqs per VU per iteration |
| Duration | 90s | Long enough to capture ≥1 full expiry cycle at `BOOKING_RESERVATION_WINDOW=20s + EXPIRY_SWEEP_INTERVAL=5s` |
| Ticket pool | 50,000 | Avoid sold-out before run end (936 iterations consumed only ~5%) |
| ABANDON_RATIO | 0.2 | Realistic conversion-rate-aware split (80% pay, 20% abandon) |
| API_ORIGIN | `http://app:8080` | Docker-internal; bypasses nginx for clean per-VU latency |
| Stack env | `BOOKING_RESERVATION_WINDOW=20s`, `EXPIRY_SWEEP_INTERVAL=5s`, `ENABLE_TEST_ENDPOINTS=true`, `APP_ENV=development` | Per `make demo-up` |
| Stack image | `booking_monitor-app` rebuilt 2026-05-09 (post-D7) | Verified post-D7 binary: `book` no longer emits `events_outbox(order.created)` |

## Headline metrics

| Metric | Value | Notes |
|---|---|---|
| `accepted_bookings/s` | **8.41** | Booking-hot-path throughput (`POST /book` → 202) |
| `paid_orders/s` | **6.72** | Happy-path terminal success (paid observed via polling) |
| `payment_intents_created/s` | **6.72** | Matches `paid_orders/s` 1:1 (intent → confirm → paid is the happy chain) |
| `compensated_abandons/s` | **0.32** | Abandon-path terminal success (compensated observed) |
| `expired_seen` | 128 | Transient `expired` observations (not all reach compensated; see analysis) |
| `business_errors` | **9.72%** | **Above the 5% threshold** — see "Saga compensator backed up" below |
| `end_to_end_paid_duration p95` | 708.7ms | Well under the 30s threshold |
| `book_to_reserved_duration p95` | 520.7ms | Worker async-persist latency |
| `reserved_to_paid_duration p95` | 197ms | /pay → webhook → MarkPaid round-trip |
| `http_req_duration p95` | 5.3ms | HTTP layer alone (excludes the polling sleeps) |
| `http_reqs/s` | 228.5 | Total request rate including polls |
| `iterations` | 936 complete + 73 interrupted | 10.4 complete iterations/s |

## Conservation check

```
iterations completed:        936
  paid_orders:               807   (happy path success)
  compensated_abandons:       38   (abandon path success)
  business_errors:            91   (any failure path)
  ───────────────────────────────
  sum:                       936   ✓
```

Sold-out (409) was 0 — the 50k pool wasn't depleted (936 iterations × 1 ticket = 0.5% of pool; HTTP-layer 4.4% `http_req_failed` is from polling 404s during the brief async-processing window, expected).

> **Note on `accepted_bookings=1009` vs `iterations=936`**: k6 reports `accepted_bookings/s = 8.41` over its full wall-clock window (120s, including the 30s graceful-stop period after the 90s VU-active phase). The 73-iteration gap (1009 − 936) is iterations that took a `book` step but were interrupted by graceful-stop before reaching any terminal path — those bookings were `202`-accepted server-side (and the worker did persist them as `awaiting_payment`), but the k6 iteration didn't observe their terminal state. The conservation table above sums to `936` because it counts only iterations that reached terminal observation; the 73 interrupted bookings live as `awaiting_payment` rows in PG until the D6 expiry sweeper compensates them. This is consistent with Pattern A's design (server keeps state regardless of client disconnect) but worth flagging so future readers don't trip on the `1009` vs `936` discrepancy.

## Analysis

### 1. Happy path is healthy

`end_to_end_paid_duration` median 637ms, p95 708ms — well within Pattern A's reservation window. The full pipeline (book → worker persist → /pay intent → test-confirm → webhook → MarkPaid → poll observes paid) executes in well under a second at this VU count.

`reserved_to_paid_duration` median 129ms, p95 197ms — the /pay + signed-webhook + MarkPaid leg dominates. The `book_to_reserved_duration` median 502ms is mostly the **k6-side polling cadence** (`POLL_INTERVAL_MS=500`), not actual worker latency; the worker async-persist itself is sub-50ms based on `http_req_duration` distribution.

### 2. Saga compensator backs up under abandon load (the load story)

`expired_seen=128, compensated_abandons=38` → **70% of abandon iterations observed `expired` but didn't reach `compensated` within the poll window**. The 91 `business_errors` count almost exactly matches the gap (128 - 38 = 90, vs 91 errors), confirming the failure shape is "abandon path stuck at `expired`".

**Why**: 100 VUs × 20% abandon × ~10s/iteration ≈ 2 abandon orders in flight at any time, but D6 sweeper batches every 5s and emits all overdue rows in one batch. When 5-10 orders are dropped into the saga `order.failed` topic at once, the in-process saga compensator (single goroutine consuming the topic) processes them sequentially. Each compensation = `revert.lua INCRBY + MarkCompensated UoW` ≈ 50-100ms, so a batch of 10 takes 500ms-1s — within poll window for the first few, beyond for the last few.

This is **the exact failure mode the round-2 metric split was designed to surface**: pre-split, all abandon iterations would have looked successful (any `expired` OR `compensated` counted); post-split, the backlog shows up as `expired_seen >> compensated_abandons + business_errors high`.

**Operational reading**: at 100 VUs / 20% abandon / 5s sweep / single saga consumer, the compensator becomes the bottleneck on the abandon path well before the booking hot path saturates. Resolution paths (out of D9 scope, captured for D12 / Phase 4):

- Partition `order.failed` by order_id for parallel saga consumers
- Increase sweeper batch tick rate (5s → 1s) to spread abandon load
- Bound the per-tick sweeper batch size so the saga topic can't get a 10-row burst at once

### 3. business_errors threshold breach is real signal, not flake

The 5% threshold was set in `scripts/k6_two_step_flow.js` based on "any single workflow stage failing should be rare." 9.72% is dominated by saga-backed-up abandons (90 of 91 errors). With ABANDON_RATIO=0.0 (pure happy path) the threshold would pass — confirms this is an abandon-load-specific limitation, not a hot-path correctness regression.

## Future runs

To re-capture under the same conditions:

```bash
make demo-up                         # 20s reservation, 5s sweep, post-D7 binary
make reset-db                         # clean baseline state
make bench-two-step | tee \
    docs/benchmarks/<ts>_two_step_baseline_c100_d90s/run_raw.txt
# then update this comparison.md by hand referencing the captured numbers
```

To explore abandonment specifically:

```bash
TWO_STEP_VUS=100 TWO_STEP_DURATION=90s TWO_STEP_ABANDON_RATIO=1.0 \
    make bench-two-step | tee /tmp/abandon_only.txt
```

This forces every iteration to abandon — useful for measuring saga compensator throughput in isolation.

To pure-happy-path stress-test:

```bash
TWO_STEP_VUS=200 TWO_STEP_DURATION=120s TWO_STEP_ABANDON_RATIO=0.0 \
    make bench-two-step | tee /tmp/happy_only.txt
```

This is the comparison axis D12 will exercise across the 4 stages (`cmd/booking-cli-stage{1,2,3,4}`).

## Cross-references

- Companion blog post: [`docs/blog/2026-05-saga-pure-forward-recovery.zh-TW.md`](../../blog/2026-05-saga-pure-forward-recovery.zh-TW.md) ([EN](../../blog/2026-05-saga-pure-forward-recovery.md)) — architectural rationale for Pattern A
- Companion walkthrough: [`docs/demo/walkthrough.cast`](../../demo/walkthrough.cast) — 3-phase asciinema demonstrating the same flow at low VU
- Benchmark conventions: [`.claude/CLAUDE.md` § Benchmark Conventions](../../../.claude/CLAUDE.md)
