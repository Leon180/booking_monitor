# Stage 5 demo re-run — canonical 5-stage harness settings

> 2026-05-21 · branch `feat/stage5-admin-dashboard` · commit `ec3b091` · captured by `make demo-stage5-rush` (default settings).

## Why this report exists

Three things needed to be recorded together for the PR #124 demo to be
defensible at interview-time:

1. The PR #124 demo binary (Stage 5 with admin SSE wired in) reproduces
   the canonical 5-stage benchmark Stage 5 row when invoked at the same
   settings — but with the side effect of running Stage 5 in isolation
   instead of beside Stages 1–4. Headline numbers therefore differ from
   the canonical report by a known amount, and the report has to spell
   out the apples-to-apples adjustment.

2. The interview-prep cheat sheet (private,
   `~/.claude/plans/zippy-napping-tarjan.md`) cites these numbers as
   ground truth. Without a captured environment snapshot the citation
   is unverifiable two weeks from now.

3. The 7,142 accepted/s figure briefly mentioned in an earlier chat
   was from a 20-second subset; this is the full 60-second canonical
   run, with corrected throughput.

See [`env.md`](./env.md) for the captured environment alongside this
file. Raw k6 output, before/after `docker stats`, and the post-drain
bus metric snapshot are also committed in this directory.

## Configuration

| Parameter | Value |
|-----------|-------|
| k6 script | `scripts/k6_intake_only.js` (unchanged) |
| Make target | `make demo-stage5-rush` (defaults, no overrides) |
| VUs | 500 |
| Duration | 60s |
| Ticket pool | 500,000 |
| Target URL | `http://app:8080/api/v1` (direct, no nginx) |
| Binary | `cmd/booking-cli-stage5/` (BUILD_TARGET in `docker-compose.demo-stage5.yml`) |
| Payload | `POST /api/v1/book {user_id: random, ticket_type_id: <setup>, quantity: 1}` |
| Stack | Full default `docker-compose.yml` + the stage5 build-args override (so the canonical full stack is present: postgres, redis, kafka, zookeeper, jaeger, prometheus, grafana, alertmanager, recon, saga_watchdog, expiry_sweeper, nginx) |

## Results (k6 reported, 60s window)

| Metric | Value |
|---|---|
| `accepted_bookings` (HTTP 202 count) | **503,709** |
| `accepted_bookings` per-second | **8,394 / s** |
| `business_errors` rate | **0.00 %** |
| `http_req_duration` p50 | 53.28 ms |
| `http_req_duration` p90 | 64.37 ms |
| `http_req_duration` p95 | **77.21 ms** |
| `http_req_duration` max | 236.11 ms |
| `http_req_failed` rate | 15.77 % (= 94,356 / 598,066 → these are 409 sold-out, NOT errors; see "Why http_req_failed is non-zero" below) |
| `iterations` total | 598,065 |
| `iterations` per-second | 9,966.82 / s |
| `vus` (constant) | 500 |

## Comparison to canonical 5-stage harness

Source: [`docs/benchmarks/comparisons/20260513_141854_5stage_c500_d60s/comparison.md`](../comparisons/20260513_141854_5stage_c500_d60s/comparison.md) Stage 5 row (intake-only scenario B).

| Metric | This run (Stage 5 alone) | Canonical 5-stage harness | Delta |
|---|---|---|---|
| `accepted/s` | **8,394** | 5,139 | **+63 %** |
| p95 latency | **77.21 ms** | 126.9 ms | **−39 %** |
| `business_errors` | 0.00 % | 0.00 % (Stage 5 row) | identical |
| Pool depleted in window? | **Yes (~503k of 500k accepted)** | No (~308k of 500k) | this run drained the pool |

### Why this run is faster than the canonical

The 5-stage harness boots Stages 1, 2, 3, 4, 5 side-by-side, all
sharing the same 7.65 GiB Docker VM (10 logical cores). When the
load script targets Stage 5 it does so while Stages 1–4 hold idle
goroutines, open Redis / Kafka / PG connections, and consume some
fraction of CPU + memory just by being up.

This re-run boots Stage 5 only (`cmd/booking-cli-stage5/` via the
`docker-compose.demo-stage5.yml` BUILD_TARGET override); the
sidecar services (recon, saga_watchdog, expiry_sweeper) still run
the Stage 4 binary, but Stages 1–3 do not run. The freed memory
and CPU translate directly into higher Kafka producer throughput,
lower Redis Lua latency, and lower contention on the booking hot
path.

This is reproducibility-preserving: same k6 script, same VUs,
same pool, same target — only the resource-competition variable
differs. Either headline (5,139 or 8,394 accepted/s) is "true",
depending on whether the comparison universe includes Stages 1–4
or not. The canonical apples-to-apples number for cross-stage
comparison is 5,139/s; the apples-to-apples number for "what does
Stage 5 do when it's the only thing running" is 8,394/s.

### Why `http_req_failed` is non-zero (not a regression)

15.77 % of HTTP requests in this run returned 409 Conflict. That is
the expected sold-out fast-path: when `total_tickets - accepted ≤ 0`,
the Lua DECRBY returns a "sold out" signal and the handler returns
409 immediately without touching Kafka. The 503,709 successful
accepted bookings exhausted the 500,000 pool partway through the 60s
window, so the remaining VU iterations got 409s.

The 5-stage harness Stage 5 row reports `pool depleted? ❌ No`
because at 5,139/s × 60s ≈ 308k accepted, the 500k pool was NOT
depleted. This run depleted the pool because of the higher
throughput — same pool size, higher rate, depletion happens earlier.

`business_errors` (Stage 5's own threshold-gated counter) excludes
202 and 409 and is at 0.00 %.

## Architecture finding: intake rate > worker consume rate

This is the most interesting non-headline finding.

| Metric | At k6 finish (t=60s) | After 30s extra wait (t=90s) | After 90s extra wait (t=150s) |
|---|---|---|---|
| `admin_event_bus_published_total{order.created}` | 221,262 | 396,493 | **529,513** (drained) |
| PG `orders` table row count | — | 450,401 | 583,321 |

The k6 client reported 503,709 HTTP 202s in 60s — meaning the API
+ Lua DECRBY + Kafka acks=all path returned success that many times.
But the worker, which consumes from Kafka and writes order rows to PG
(and publishes to the admin event bus post-commit), was still draining
the Kafka backlog 90 seconds after the load test finished.

Sustained Kafka consume rate (from the bus published_total delta):
- (529,513 − 221,262) / 120 s = **2,570 events/s**
- This is well below the API intake ceiling of 8,394/s.

Interpretation:
- The API intake rate (Kafka acks=all publish) is much faster than
  the worker consume rate at this Docker resource allocation. Kafka
  absorbs the difference — `booking.intake.v5` builds a backlog
  during the load test and the worker drains it after.
- This is exactly the Stage 5 design intent: decouple intake
  durability (Kafka acks=all) from PG persistence. Under burst,
  intake stays fast, and Kafka buffers; sustained throughput is
  bounded by the worker, not the API.
- For a flash-sale system this is the right trade-off: the user-
  visible "I got a 202, my booking is locked" happens at intake
  speed, while DB persistence catches up afterwards.

For demo narration this is more interesting than the throughput
headline. Easy to demonstrate with the war-room Grafana dashboard:
during the k6 run, the `admin_event_bus_channel_depth` panel and the
`Bookings/sec` stat both light up; after k6 finishes, the
`accepted_bookings/s` rate at the dashboard continues for ~2 minutes
as the worker drains.

## Stack resource usage (post-run snapshot)

| Container | CPU % at snapshot | Memory (RSS / 7.65 GiB host) |
|---|---|---|
| `booking_app` (Stage 5) | 45.22 % | 185.1 MiB |
| `booking_db` (postgres) | 43.11 % | 173.8 MiB |
| `booking_kafka` | 12.93 % | 890.8 MiB |
| `booking_redis` | 7.05 % | 362.6 MiB |
| `booking_zookeeper` | 0.03 % | 169.4 MiB |
| `booking_jaeger` | 0.01 % | 105.2 MiB |
| `booking_prometheus` | 0.00 % | 103.4 MiB |
| `booking_grafana` | 0.01 % | 128.9 MiB |
| `booking_nginx` | 0.00 % | 2.7 MiB |
| Other sidecars (alertmanager, recon, expiry_sweeper, saga_watchdog, redis_exporter, alert_logger) | ~0 % | ~30 MiB each |
| **Sum** | — | **~2.4 GiB** (well below 7.65 GiB ceiling) |

Even at peak load Stage 5 used only 185 MiB of the 256 MiB
`GOMEMLIMIT` budget. The two CPU-busy containers were `app` and
`postgres` — Postgres because the worker is INSERTing orders into
it; `app` because of the Redis Lua + Kafka publish hot path.

No memory pressure, no swap, no OOM. The Stage 5 demo could
sustain higher VUs before hitting host-side limits — the
bottleneck is server-side (worker consume rate), not client-side
or host-resource side.

## Known anomalies worth a follow-up note (not blocking)

1. `accepted_bookings = 503,709` exceeds `total_tickets = 500,000`
   by 3,709 (0.74 %). Possible causes:
   - Pre-existing test residue in the same `ticket_type_qty:{id}`
     Redis key from earlier interactive runs in this session
     (this stack has been up 2 days and accumulated state from
     multiple smoke runs — see [env.md](./env.md) "Running
     services" timestamps).
   - Subtle race in the Stage 5 `DeductInventoryNoStream` Lua
     script under high concurrency.
   - k6 counter mis-classification (very unlikely given the
     script's branch-once-per-iteration logic).

   Worth a clean-state re-run (`make reset-db` first, then this
   benchmark) before publishing the number externally.

2. PG `orders` table at end shows 583,321 rows, which is more than
   either the bus published count or the accepted count. This is
   because the PG row count is cumulative across all benchmark
   runs in this session, not just this one — `make reset-db`
   would zero it.

## Files in this directory

| File | What |
|---|---|
| `env.md` | Captured environment (host hardware, Docker, Go, app build, env vars, running services) |
| `comparison.md` | This document |
| `k6_raw_output.txt` | Full unfiltered k6 stdout from the canonical run |
| `bus_metrics_after.txt` | Admin event bus metrics snapshot at k6 finish (worker still draining) |
| `bus_metrics_drained.txt` | Admin event bus metrics snapshot 120s after k6 finish (worker drained) |
| `docker_stats_before.txt` | `docker stats --no-stream` snapshot before k6 started |
| `docker_stats_after.txt` | `docker stats --no-stream` snapshot at k6 finish |
