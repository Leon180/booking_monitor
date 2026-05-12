# Go pprof analysis — finding the real bottleneck

**Date**: 2026-05-12
**Hardware**: Apple Silicon MacBook + Docker Desktop
**Build**: booking_monitor `feat/d4.1-kktix-ticket-type` HEAD
**Method**: 30s CPU profile via `wget http://127.0.0.1:6060/debug/pprof/profile?seconds=30` inside `booking_app` container during k6 load
**Artifacts**:
- [`pprof_sold_out_phase.prof`](pprof_sold_out_phase.prof) — 50k pool, sold out within 1.5s, 99% of profile is sold-out path
- [`pprof_pre_depletion_5M_pool.prof`](pprof_pre_depletion_5M_pool.prof) — 5M pool, profile covers ~50/50 accept/sold-out mix

## TL;DR

**The 8,330 acc/s figure in [`docs/blog/2026-05-lua-single-thread-ceiling`](../../blog/2026-05-lua-single-thread-ceiling.zh-TW.md) is stale; today's measurement shows the system runs at 28,000 acc/s steady state**. The real CPU bottleneck identified by pprof is **`log.Error` on the sold-out path** consuming **28% of total CPU**, more than the actual booking logic itself (24.8%).

Combined with the earlier Redis baseline finding(deduct.lua server-side does 273k RPS),the blog's "Lua single-thread on same key serialization is the physical ceiling" diagnosis is empirically incorrect on three counts:
1. The ceiling is not 8,330 (current measurement: 28k acc/s, 3.4× higher)
2. Redis-side Lua is not saturated (273k RPS isolated capability)
3. The dominant CPU consumer is logging, not Redis

## Test conditions

| Profile | Pool | VUs | Duration | Profile timing |
|:--|:--|:--|:--|:--|
| `pprof_sold_out_phase` | 50,000 tickets | 500 | 120s | Captured at t=15s (pool drained by t=1.5s, so 28.5s of 30s window is post-depletion) |
| `pprof_pre_depletion_5M_pool` | 5,000,000 tickets | 500 | 60s | Captured at t=12s (pool not yet drained, accept/sold-out ratio ~50/50) |

k6 final tally for second run:
- **Total iterations**: 3,244,947 in 60s → **54,073 req/s**
- **Accepted (202)**: 1,665,669 → **27,761 acc/s**(steady state)
- **Sold-out (409)**: 1,579,279 → 26,321 req/s
- **p95 latency**: 12.81 ms

## CPU breakdown (pre-depletion profile)

Total samples: 83.10s in 30s wall = 2.77 cores active.

| Layer | CPU time | % of total | Notes |
|:--|--:|--:|:--|
| `net/http.(*conn).serve` | 75.73s | 91.13% | All HTTP request handling |
| `gin.Engine.ServeHTTP` | 54.16s | 65.17% | Gin framework + middleware |
| `bookingHandler.HandleBook` cumulative | 50.29s | 60.52% | Full booking handler |
| ├ `service.BookTicket` (real booking logic) | 20.58s | 24.8% | Redis Lua + business logic |
| ├ **`log.Error("BookTicket failed", ...)`** | **23.51s** | **28.3%** | **Hotspot — sold-out path** |
| ├ `ShouldBindJSON` (parse request) | 3.83s | 4.6% | JSON unmarshal |
| ├ `c.Data` + `mustMarshal` (write response) | 2.03s | 2.4% | Error response build |
| `redisInventoryRepository.DeductInventory` | 17.74s | 21.3% | Lua call via go-redis (inside `service.BookTicket`) |
| `go-redis Script.Run` + `EvalSha` | 16.61s | 20.0% | go-redis client RTT to Redis |
| Idempotency middleware | 49.65s cum | 60.0% | Wraps everything below — minimal flat cost (~0.1s) |
| `Metrics` middleware | 50.67s cum | 61.0% | Wraps everything below — minimal flat cost (~0.05s) |

Flat (leaf node) view shows:
- `internal/runtime/syscall.Syscall6`: 27.19s (32.7%) — combined network writes + log writes
- `zap.safeAppendStringLike`: 2.89s (3.6%) — log serialization
- `bufio.(*Writer).Flush`: 18.47s cumulative — log buffer flushes

Approximation: **of the 27.19s syscall time, ~18s is log-write flushes; ~9s is network response writes**. So actual log overhead (log.Error chain + log-write syscalls) is closer to **40% of total CPU** when you include the syscall.Write portion attributable to log flushing.

## Code path — line-by-line CPU

[`internal/infrastructure/api/booking/handler.go:160-187`](../../../internal/infrastructure/api/booking/handler.go):

```
210ms  50.29s  func (h *bookingHandler) HandleBook(c *gin.Context) {
                ctx := c.Request.Context()

   .   80ms    var req dto.BookingRequest
   .   3.83s   if err := c.ShouldBindJSON(&req); err != nil {  ← JSON parse
                    log.Warn(ctx, "invalid book request body", tag.Error(err))
                    c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid request body"})
                    return
                }

  20ms   20.58s    order, err := h.service.BookTicket(...)    ← actual booking work
                  if err != nil {
   .    23.51s        log.Error(ctx, "BookTicket failed",     ← ⚠️ HOTSPOT: 28% of total CPU
   30ms   30ms          tag.Error(err),
   10ms   10ms          tag.UserID(req.UserID),
   90ms  120ms          tag.TicketTypeID(req.TicketTypeID),
   10ms   10ms          tag.Quantity(req.Quantity),
   .                  )
   30ms   80ms          status, publicMsg := mapError(err)
   10ms   2.03s         c.Data(status, "application/json",
                                mustMarshal(dto.ErrorResponse{Error: publicMsg}))
                       return
                   }
```

## Root cause analysis

**Every booking that returns `ErrSoldOut` triggers `log.Error` with 4 typed tags (Error/UserID/TicketTypeID/Quantity).**

`log.Error` cost breakdown (from peek):
- `zap.Logger.Check`: 12.36s (54.6%)
- `zap.CheckedEntry.Write`: 9.78s (43.2%) — this fans out to syscall.Write
- `enrichFields`: 0.47s (2.1%)

`zap`'s structured logging is fast (~14µs/call here based on 23.51s / 1.66M sold-out reqs), but at 26k sold-out/s, the aggregate cost dominates everything else in the handler.

**Why this is a real bug, not just an observation**:
1. `ErrSoldOut` is an **expected business condition**, not an error. Logging it at `Error` level conflates "system failure" with "inventory exhausted".
2. Production observability — if you alert on `level=error`, this floods your alerting during a sold-out event(the very moment ops attention is most valuable).
3. Disk / log-pipeline overhead — at 26k sold-out/s × ~200 bytes per Error log = 5 MB/s of structured logs purely for sold-out. Over a 60s flash sale that's 300 MB.

## Comparison of two profiles

| Metric | sold_out_phase | pre_depletion_5M | Observation |
|:--|--:|--:|:--|
| Total samples / 30s wall | 80.53s | 83.10s | Both ~2.7 cores busy |
| log.Error % | 28.13% | 28.16% | **Identical** — confirms log path dominates regardless of accept ratio |
| service.BookTicket % | 25.11% | 24.83% | Same |
| Lua call (DeductInventory) % | 22.03% | 21.34% | Same |
| Idempotency middleware flat | 0.1s | 0.05s | Negligible direct cost |

**Two profiles converge on the same finding**: the bottleneck is invariant of accept/sold-out ratio because **even the happy path's `service.BookTicket` is similar cost to the sold-out path** (both hit Redis Lua once). The 28% log.Error overhead just lands wherever sold-out responses are returned.

## What does this mean for the architecture story

### Stale claims to retract

1. **"8,330 acc/s is the same-key Lua serialization physical ceiling"** — current measurement is 28k acc/s, 3.4× higher. Either the system improved post-D7 / post-cache-rehydrate work, or the original measurement was on a different config. Either way the headline number is no longer 8,330.

2. **"Lua single-thread × single key is the bottleneck"** — Redis baseline benchmark proves deduct.lua does 273k RPS server-side. Today's pprof confirms the application-side bottleneck is `log.Error`, not Redis.

3. **"Tier 1-4 (network/client/io-threads) don't change acc/s"** — partially still true (network NAT is not the bottleneck today either), but for a different reason than the blog claims.

### What IS the bottleneck

**`log.Error` on the sold-out path eating 28-40% of CPU.**

This is a **structured logging volume** issue, not a Redis or architecture issue. **Demoting `ErrSoldOut` / `ErrTicketTypeSoldOut` from `log.Error` to `log.Debug` (or no-log) would free up ~28% of CPU**, potentially lifting throughput from 28k acc/s to ~36-40k acc/s (assuming linear).

### What this DOES validate from the blog

- The saturation profile methodology was sound (looked at multiple resources)
- The decision to focus on business-axis sharding (Layer 1: per-section) before infra-axis sharding (Layer 2: hot-section quota) is correct
- The principle "measure don't assume" is reinforced — the original "Lua serialization" diagnosis was assumption, not measurement;today's pprof is the actual measurement

## Recommended action items

### Code fix (1-2 day PR)
1. Change [`internal/infrastructure/api/booking/handler.go:175`](../../../internal/infrastructure/api/booking/handler.go) to branch on error type:
   - `domain.ErrSoldOut` / `domain.ErrTicketTypeSoldOut` → no log, or `log.Debug`(business state, not error)
   - `domain.ErrInvalidUserID` / etc. → `log.Warn`(client bug)
   - Everything else → keep `log.Error`(real failure)
2. Same applies to [`handler.go:343`](../../../internal/infrastructure/api/booking/handler.go) (`CreatePaymentIntent`) and similar paths
3. Re-benchmark — expect 30-40% throughput improvement

### Documentation
4. **Rewrite `docs/blog/2026-05-lua-single-thread-ceiling.{md,zh-TW.md}` post-fix**:
   - Drop "8,330 physical ceiling" framing
   - Add "I was wrong — here's the actual root cause I found via pprof" arc
   - Cross-link to baseline benchmark + pprof artifacts
   - **This story is stronger for senior interviews than the original**: "I had a hypothesis, ran isolated baseline benchmark + pprof, proved myself wrong, found the real bottleneck was logging, fixed it, measured the improvement"

### Optional follow-ups
5. Add sampling to Error logs(prevent flood)
6. Configure zap to drop log entries when buffer is full (vs blocking)
7. Move structured log emit off-handler-thread via async channel
