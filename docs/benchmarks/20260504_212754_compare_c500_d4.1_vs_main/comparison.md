# D4.1 vs main — Benchmark Comparison

**Date**: 2026-05-04
**Run A baseline**: `main` @ `f210e5e` (docs(design): KKTIX ticket-type alignment — pricing + hold pattern design notes)
**Run B under-test**: `feat/d4.1-kktix-ticket-type` @ `cec4e9d` (after 12 D4.1 commits + 5 review rounds)

## Test Conditions (identical for both runs)

| Setting | Value |
| :--- | :--- |
| Script | `scripts/k6_comparison.js` |
| Ticket pool | 500,000 (realistic flash-sale-event scale) |
| user_id range | 1 – 9,999,999 |
| Quantity | 1 per request |
| VUs | 500 |
| Duration | 60s |
| Target | `http://app:8080/api/v1` (direct, bypass nginx limit) |
| Stack | docker-compose, MacBook host |

Both runs reset Redis (FLUSHALL) + Postgres (TRUNCATE orders / reset events) before launch. RUN B additionally truncates `event_ticket_types` because D4.1 shipped that table; pre-D4.1's TRUNCATE list didn't include it.

## Results

| Metric | Run A (main) | Run B (D4.1) | Δ | Verdict |
| :--- | ---: | ---: | ---: | :--- |
| **accepted_bookings/s** | **8,330** | **8,331** | **+0.0%** | ✅ Load-bearing throughput unchanged |
| Total HTTP RPS | 48,351 | 28,862 | **−40.3%** | 🟡 Sold-out fast path slowed |
| p95 latency | 14.4 ms | 28.1 ms | +95% | 🟡 Still 17× under 500ms threshold |
| avg latency | 5.33 ms | 10.93 ms | +105% | 🟡 Same root cause as p95 |
| Booking accepted (success path) | 500,014 | 500,003 | -0.0% | ✅ Inventory clears cleanly |
| Business errors | 0.00% | 0.00% | — | ✅ Both clean |
| Total iterations | 2,902,308 | 1,732,078 | −40% | follows total RPS |

## Interpretation

### ✅ The load-bearing metric — `accepted_bookings/s` — is unchanged

`accepted_bookings/s` is the rate at which the system actually persists reservations through Redis Lua deduct + worker queue + DB INSERT. Both runs converge on **~8,330/s**. This is the **Lua single-thread serialization ceiling** documented in `docs/design/ticket_pricing.md §9` — Redis processes deduct.lua sequentially, and that's the upper bound on accepted-booking throughput regardless of what the API layer does.

D4.1 added wire-format work (3 extra ARGV slots in deduct.lua, 3 extra fields parsed at the worker boundary, 1 extra Postgres `GetByID` lookup before Lua) without touching the Lua hot path itself. The 0.01% delta on accepted_bookings/s is run-to-run noise (PR #37 noise floor: ~3% RPS, ~6% p95 — we're well inside that).

### 🟡 Total HTTP RPS regressed 40% — and we can explain exactly why

Pre-D4.1 `BookingService.BookTicket` flow:
```
1. Mint UUIDv7              (~µs)
2. domain.NewReservation    (in-memory)
3. Redis Lua deduct          (~5ms — Redis single-thread)
   → 1 (accepted) or -1 (sold out)
```

Post-D4.1 flow:
```
1. Mint UUIDv7
2. ticketTypeRepo.GetByID(ctx, ticketTypeID)   ← NEW: Postgres SELECT (~3-5ms)
3. Compute amount_cents = priceCents × quantity
4. domain.NewReservation
5. Redis Lua deduct
```

Every booking attempt — including ones that will end up sold-out (the 409 path) — now pays for one synchronous Postgres round-trip on the way in. At 500 VUs after the pool depletes (~17% accepted, 83% sold-out), that round-trip is on the **majority** path. The sold-out path's per-iteration cost roughly doubled, which explains the 40% RPS drop and the doubled p95.

This was flagged in review #5 (go-reviewer M4: "Hot path now has an unconditional synchronous Postgres round-trip per booking"). The reviewer suggested **benchmark-then-decide caching**. This run is that data.

### What the regression does NOT mean

- **Not a correctness regression.** Business errors stay at 0%. All 500,000 tickets clear cleanly. The state machine + saga + outbox + idempotency contracts are unchanged.
- **Not a Lua bottleneck regression.** accepted_bookings/s is identical. The system's "real" throughput at the load-bearing path is preserved.
- **Not customer-visible at realistic load.** Even D4.1's degraded p95 (28ms) is **17× under** the 500ms threshold the project's threshold pins. A real customer hitting `POST /api/v1/book` perceives sub-100ms either way.

## Recommended follow-up — cache the ticket_type lookup in Redis

The `ticket_type` for a given event almost never changes during a sale (price is fixed, `event_id` is fixed, `currency` is fixed). Caching `(ticketTypeID → {eventID, priceCents, currency})` in Redis with a 5-minute TTL would:

- Hit the cache on >99% of booking attempts (cold miss only on the first booking after deploy / cache eviction).
- Recover the pre-D4.1 Redis-only hot path for the sold-out 409 majority case.
- Add one Redis `GET` (~0.3ms) per booking instead of one Postgres round-trip (~3-5ms) — net latency saving of ~2-5ms / booking.

The Redis client + lifecycle is already wired into the booking service via `inventoryRepo`. A thin `TicketTypeCache` decorator over `domain.TicketTypeRepository` would land cleanly without touching application logic. Estimated PR size: ~150 LOC + benchmark report. Logged for the post-D4.1 follow-up.

## Methodology Caveats

1. **MacBook host, Docker for Desktop.** Production numbers will differ — what we're measuring is the *delta between branches under identical conditions*, not absolute throughput.
2. **DB has 000014 already applied for both runs.** Pre-D4.1 binary doesn't query the renamed columns or new fields, so this works; the alternative (apply migration between runs) would create unrelated noise.
3. **No noise-floor confirmation pass.** A single A/B is sometimes inside the per-run variance band (PR #37 saw ~3% RPS / ~6% p95 jitter on identical-binary back-to-back runs). The 40% RPS drop and 95% p95 increase are far outside that band — the regression is real, not noise.
4. **Single-VU-count snapshot.** `accepted_bookings/s` ceiling is Lua-bound and won't change with more VUs; the *scaling* of the total-RPS regression is left as future work (likely worse at higher VU counts as PG connection pool contention compounds).

## Raw Outputs

- [run_a_main_raw.txt](run_a_main_raw.txt)
- [run_b_d4.1_raw.txt](run_b_d4.1_raw.txt)
