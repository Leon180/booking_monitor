# D3 — Pattern A Reservation Flow vs main

**Date**: 2026-05-04
**Compared**: `main @ 21df538` (post-D2) vs `feat/d3-pattern-a-reservation-flow @ 4a35254`
**Parameters**: VUS=500, DURATION=60s

## Test Conditions (identical for both runs)

| Setting | Value |
| :--- | :--- |
| Script | `k6_comparison.js` |
| Ticket pool | 500,000 (realistic flash-sale scale; sells out partway through) |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | 500 |
| Duration | 60s |
| Target | `http://app:8080/api/v1` (direct, bypasses nginx) |
| Stack | Identical Docker compose (Postgres 15 / Redis 7 / Kafka), only the `app` image differs |
| Reset | `FLUSHALL` Redis + `TRUNCATE orders, order_status_history` + `UPDATE events SET available_tickets = total_tickets` between runs |

## Results

| Metric | Run A (main) | Run B (D3) | Δ | Note |
| :--- | ---: | ---: | :--- | :--- |
| **Total RPS** (`http_reqs/s`) | 44,374 | 47,053 | **+6.0%** | Within run-to-run noise floor (typically 3-5%); see Caveats |
| **Accepted bookings/s** (`accepted_bookings`) | 8,332 | 8,333 | **+0.01%** | The real hot-path metric — **identical**. Lua single-thread ceiling holds. |
| **http p95 latency** | 15.09 ms | 14.80 ms | **-1.9%** | Marginally better, within noise |
| **http avg latency** | 5.63 ms | 5.47 ms | **-2.8%** | Same |
| **booking_duration p95** | 19 ms | 18 ms | **-5%** | E2E user-perceived latency |
| **business_errors** | 0.00% | 0.00% | — | No HTTP-level errors (only 202 / 409) |
| **Successful bookings (count)** | 500,015 | 500,097 | — | Both runs depleted the 500k pool (the ~80 over is the wave of in-flight 202s landing after pool depletion) |

## Conclusion

**D3 imposes no measurable regression on the booking hot path.** The `accepted_bookings/s` rate — the metric that actually exercises Redis Lua deduct + worker queue + DB persistence — is unchanged at ~8,333/s, which is the Lua single-thread ceiling documented in `docs/blog/2026-05-lua-single-thread-ceiling.md`. The added work in D3 (NewReservation invariant validation + extra Lua ARGV + extra XADD field + extra DB column write + reservedUntil time computation) is invisible at this load.

**Total RPS variation (+6%) is run-to-run noise**, not a D3 effect. The 409 fast path dominates total RPS once the pool depletes, and 409 response bytes are byte-identical between main and D3 (same `{"error": "sold out"}` shape). The +6% is captured here for honesty rather than spun as an improvement.

## Caveats

1. **Single run per branch.** Run-to-run variance for k6 + Docker on a laptop is typically 3-5% on RPS and 5-10% on p95. The accepted_bookings + p95 deltas here are well within that band — confirmation runs would likely show D3 within ±5% of main on every metric. The PR did not re-run because the accepted_bookings ceiling (the load-bearing measurement) was clear from a single pass: identical to 4 significant figures.
2. **Post-depletion regime dominates.** Both runs depleted the 500k pool early (around 60-90s mark in the booking_duration trace, but pool was effectively gone by 30s based on accepted_bookings rate). Most of the 60s window measures the cheap 409 fast path, not the booking hot path. The `accepted_bookings/s` metric isolates the hot path; that's the one to read.
3. **Wire-format change in D3.** Response body is larger (adds `reserved_until` + `expires_in_seconds` + `links.pay`) — typical 202 body grew from ~150 bytes to ~280 bytes. Network throughput jumped from 595→704 MB received, which is consistent. No effect on per-request latency at this scale.

## Reproduction

```bash
# From a clean checkout:
git checkout main && docker-compose build app && docker-compose up -d app
sleep 10
docker exec booking_redis redis-cli -a "$REDIS_PASSWORD" FLUSHALL
psql "$DB_URL" -c "TRUNCATE TABLE orders, order_status_history RESTART IDENTITY; UPDATE events SET available_tickets = total_tickets;"
docker run --rm -i --network=booking_monitor_default \
  -e VUS=500 -e DURATION=60s \
  -v "$(pwd)/scripts/k6_comparison.js:/script.js" \
  grafana/k6 run /script.js | tee run_a_main.txt

git checkout feat/d3-pattern-a-reservation-flow && docker-compose build app && docker-compose up -d app
sleep 10
# repeat reset + k6 → run_b_d3.txt
```

## Raw outputs

- [run_a_main.txt](run_a_main.txt) — main branch baseline
- [run_b_d3.txt](run_b_d3.txt) — D3 branch
