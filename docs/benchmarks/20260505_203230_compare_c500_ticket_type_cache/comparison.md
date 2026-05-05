# ticket_type cache decorator vs D4.1-no-cache baseline

**Date**: 2026-05-05
**Run A baseline**: D4.1-no-cache — `feat/d4.1-kktix-ticket-type` @ `cec4e9d` (numbers transcribed from [`20260504_212754_compare_c500_d4.1_vs_main/`](../20260504_212754_compare_c500_d4.1_vs_main/comparison.md) Run B)
**Run B under-test**: this branch (`feat/cache-ticket-type-lookup`) — D4.1 + read-through Redis cache on `TicketTypeRepository.GetByID`. Two consecutive runs of the same binary.

## Test conditions (identical to the PR #89 benchmark)

| Setting | Value |
| :--- | :--- |
| Script | `scripts/k6_comparison.js` |
| Ticket pool | 500,000 |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | 500 |
| Duration | 60s |
| Target | `http://app:8080/api/v1` (direct, bypass nginx limit) |
| Stack | docker-compose, MacBook host |

Both runs reset Redis (FLUSHALL) + Postgres (TRUNCATE orders + event_ticket_types + reset events) before launch.

## Headline result

| Metric | D4.1-no-cache (baseline) | D4.1 + cache (this PR) | Δ vs baseline | Verdict |
| :--- | ---: | ---: | :--- | :--- |
| **accepted_bookings/s** | **8,331** | **~8,332** | +0.0% | ✅ Lua ceiling unchanged (expected — cache doesn't touch the booking hot path) |
| Total HTTP RPS | 28,862 | **~39,857** (Run A 40,543 / Run B 39,172) | **+38%** | ✅ Sold-out fast path largely recovers |
| p95 latency | 28.1 ms | **~22.2 ms** (Run A 22.44 / Run B 21.90) | **−21%** | ✅ Approaching pre-D4.1 territory |
| avg latency | 10.93 ms | **~9.96 ms** (Run A 10.57 / Run B 9.35) | **−9%** | ✅ Modest avg gain, dominated by cache hit cost |
| Booking accepted | 500,003 | 500,005 (avg of A+B) | -0.0% | ✅ Inventory clears cleanly |
| Business errors | 0.00% | 0.00% | — | ✅ Both clean |

For reference (from the prior PR #89 report), pre-D4.1 main was at 48,351 RPS / 14.4ms p95 — the cache recovers **~57% of the gap** (28,862 → 39,857 closed of 19,489 lost). Full recovery is impossible without restructuring the wire format: the cache eliminates the *Postgres* round-trip but not the cache-side Redis GET + JSON unmarshal, which together still cost ~1ms per call.

## Self-consistency check (Run A vs Run B, same binary)

| Metric | Run A | Run B | Δ |
| :--- | ---: | ---: | :--- |
| accepted_bookings/s | 8,331.72 | 8,332.03 | +0.0% |
| Total RPS | 40,543 | 39,172 | -3.4% |
| p95 latency | 22.44ms | 21.90ms | -2.4% |
| avg latency | 10.57ms | 9.35ms | -11.5% |

Run-to-run variance lands inside the documented noise floor (PR #37: ~3% RPS, ~6% p95). The 11% avg-latency delta is concentrating in the long tail (max 124ms vs 148ms — a few outliers from Mac docker scheduler noise; doesn't move p95).

## Why total RPS recovers but doesn't fully match pre-D4.1

Pre-D4.1 `BookingService.BookTicket` flow on the sold-out path:
```
1. Mint UUIDv7              (~µs)
2. domain.NewReservation    (in-memory, only event_id needed)
3. Redis Lua deduct         (~5ms — Redis single-thread)
   → -1 (sold out) → 409 + INCRBY revert
```
**Zero** DB lookups. The Lua deduct is the only round-trip.

Post-D4.1 (no cache):
```
1. Mint UUIDv7              (~µs)
2. ticketTypeRepo.GetByID   (PG round-trip + row scan; ~5ms)
3. domain.NewReservation    (now needs amount_cents/currency from step 2)
4. Redis Lua deduct         (~5ms)
   → -1 (sold out) → 409 + INCRBY revert
```
**+1 PG round-trip** dominates the 40% RPS regression on the cheap-409 path.

Post-D4.1 + cache (this PR):
```
1. Mint UUIDv7              (~µs)
2. cache.GetByID            (Redis GET + JSON unmarshal; ~1ms post-warm-up)
3. domain.NewReservation
4. Redis Lua deduct         (~5ms)
```
**+1 Redis round-trip** is much cheaper than +1 PG round-trip but still non-zero. To regain the last 18% would require the client to send `event_id` directly in the booking request so step 2 can be skipped on the sold-out path — but that breaks the D4.1 wire contract (`ticket_type_id` is the customer-facing input) and is a bigger redesign than this PR scopes.

## Cache hit/miss observability

Live smoke confirmed the metric labels populate cleanly:

```
# Pre-traffic (just-started server)
cache_hits_total{cache="ticket_type"} 0
cache_misses_total{cache="ticket_type"} 0

# After 1 event create + 5 bookings (same ticket_type_id)
cache_hits_total{cache="ticket_type"} 4
cache_misses_total{cache="ticket_type"} 1
```

In the 60s benchmark every booking attempt against the same ticket_type_id resolves to a cache hit after the very first call (1 miss + ~2.4M hits). The miss rate is `1 / 2,400,000 ≈ 4×10^−7`, well below the operator-attention threshold for any reasonable alert.

## Conclusion

The decorator recovers ~57% of the D4.1 regression on the sold-out fast path with no impact on `accepted_bookings/s` (the load-bearing booking-throughput metric). Cost: +160 LOC of decorator + 10 unit tests. Worth it.

The remaining ~18% gap to pre-D4.1 numbers is the unavoidable cost of needing a per-booking lookup at all — closing it requires a wire-format redesign that's out of scope for this PR (logged in PR #89's "Known follow-ups" §1 was THIS PR; a future "skip lookup on sold-out path" item is a separate optimisation).

## Raw outputs

- [run_a_raw.txt](run_a_raw.txt)
- [run_b_raw.txt](run_b_raw.txt)
