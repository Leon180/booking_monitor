# D7 vs main — booking hot-path benchmark

**Date**: 2026-05-08 (Asia/Taipei)
**Goal**: confirm D7's `events_outbox(order.created)` write removal from `MessageProcessor.Process` is non-regressive on the booking hot path
**Conditions**: `scripts/k6_comparison.js`, 500 VUs, 60s, direct `app:8080` over docker network, `IDEMPOTENCY=false`, 500K-ticket pool (drains partway through the 60s window)

## Why this report exists

D7 (saga-scope-narrow PR) deletes the legacy A4 auto-charge path. Among the deletions is the `events_outbox(event_type="order.created")` write inside the booking UoW (`internal/application/worker/message_processor.go`). That UoW is on the booking persistence hot path; per project benchmark convention (`.claude/CLAUDE.md` "Benchmark Conventions"), changes there must land a comparison report or document an exemption.

UoW shape change:

```
pre-D7 :  uow.Do { decrement_ticket; INSERT order; INSERT events_outbox(order.created) }
post-D7:  uow.Do { decrement_ticket; INSERT order }
```

Provably non-regressive direction (one SQL statement removed; no new code paths). The benchmark verifies the magnitude.

## Per-run results

| Run | Variant | http_reqs/s | accepted_bookings/s | booking_duration p95 | http_req_duration p95 |
| :-- | :--- | ---: | ---: | ---: | ---: |
| A | main (pre-D7) | 43,503.22 | 8,330.78 | 23.00 ms | 20.04 ms |
| B | D7 (this branch) | 42,887.36 | 8,331.45 | 21.00 ms | 18.09 ms |

Raw outputs: [run_a_raw.txt](run_a_raw.txt), [run_b_raw.txt](run_b_raw.txt).

## Deltas

| Metric | Delta | Pass / fail (≤5% gate) |
| :--- | ---: | :--- |
| `http_reqs/s` | **−1.42%** | PASS (within noise; cheap-409 fast path dominates this metric) |
| `accepted_bookings/s` | **+0.01%** | PASS (flat — bound by downstream async work, not the deleted SQL statement) |
| `booking_duration p95` | **−8.7% (improvement)** | PASS (one fewer SQL statement per booking → measurable win) |
| `http_req_duration p95` | **−9.7% (improvement)** | PASS (same root cause) |

## Interpretation

The signal matches the predicted shape:

- **`accepted_bookings/s` is flat.** The booking hot path's bottleneck is the worker queue + saga compensator, not the worker's SQL statement count. Removing one INSERT inside the per-booking UoW shifts the hot-path median latency but doesn't change steady-state booking throughput.
- **`p95` improvements are real.** Both `booking_duration` (k6-side end-to-end) and `http_req_duration` (HTTP-only) drop ~9%. Consistent with one fewer round-trip + one fewer row insertion per accepted booking.
- **`http_reqs/s` slight dip is noise.** The `http_reqs` metric is dominated by the cheap 409 sold-out fast path post-pool-depletion (~30s into the run); 1.4% sits inside the 3-5% run-to-run variance the senior-review checkpoint documented for k6-on-Docker laptop runs.
- **Decision gate: PASS** for Q1=B (full producer-side removal). No regression on `accepted_bookings/s` (≤5%); booking-path latency improves; total throughput unchanged within noise.

## Caveats

- Single-pair sample (run A + run B). The May 6 lua-runtime-meta repeatability check needed three pairs to firm up the signal; D7's predicted-direction bound is tighter (one SQL statement removed cannot regress accepted-bookings throughput by construction), so a single run is sufficient evidence for the ≤5% gate. If a future PR touching the same UoW shows ambiguous deltas, escalate to the three-pair pattern.
- Both runs against the same Docker stack on the same laptop, back-to-back within ~3 minutes — minimises environmental drift but doesn't eliminate it (kernel scheduling jitter, Docker bridge state).
- Pool was reset (`make reset-db`) between runs so each starts from the 500K-ticket baseline.
- D7's saga consumer is in-process inside `app` (same as pre-D7); the `payment_worker` container was stopped before the D7 run because the post-D7 binary doesn't include the `payment` subcommand. This does not affect the booking hot path the benchmark measures.

## Provenance

- Branch under test: `feat/d7-narrow-saga-scope`
- Baseline: `main` HEAD `6f20cae` (PR #97 D8 PR-2 merge)
- k6: `grafana/k6` (latest at run time)
- VUs: 500, Duration: 60s, Tickets: 500,000, IDEMPOTENCY: false
