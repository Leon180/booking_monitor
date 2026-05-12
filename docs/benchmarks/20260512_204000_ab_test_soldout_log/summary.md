# A/B Test — Sold-Out Log Removal Impact on Booking Throughput

**Date**: 2026-05-12
**Hardware**: Apple Silicon MacBook + Docker Desktop, 10 cores host, no CPU limit on `booking_app` container
**Method**: Two 90s k6 runs (500 VUs, 50,000-ticket pool) against the same Redis/PG stack, swapping only one line in `internal/infrastructure/api/booking/handler.go`. Mutex + block profile rates enabled via `runtime.SetMutexProfileFraction(1)` + `runtime.SetBlockProfileRate(1)` for both runs.

## TL;DR

This A/B test settles the bottleneck question. Three earlier attempts to identify the throughput cap were wrong or incomplete:

| Attempt | Hypothesis | Verdict |
|:--|:--|:--|
| 1 (blog post) | "Lua single-thread on same key serialisation is physical ceiling at 8,330 acc/s" | ❌ **Wrong** — Redis baseline shows deduct.lua does 273k RPS, system today does 46k+ req/s |
| 2 (first CPU pprof) | "log.Error eats 28% CPU therefore it's the bottleneck" | ⚠️ **Misleading** — CPU was only at 29% of available (no container limit), so high CPU% on a function doesn't prove it caps throughput |
| 3 (block profile + A/B) | "log.Error causes `internal/poll.fdMutex.writeLock` contention on shared stdout FD" | ✅ **Confirmed** — block profile shows 97.77% of HandleBook block time is in log.Error→fdMutex; A/B test confirms +28% throughput when removed |

## Code change tested

```diff
 order, err := h.service.BookTicket(ctx, req.UserID, req.TicketTypeID, req.Quantity)
 if err != nil {
-    log.Error(ctx, "BookTicket failed",
-        tag.Error(err), tag.UserID(req.UserID),
-        tag.TicketTypeID(req.TicketTypeID), tag.Quantity(req.Quantity),
-    )
+    if !errors.Is(err, domain.ErrSoldOut) && !errors.Is(err, domain.ErrTicketTypeSoldOut) {
+        log.Error(ctx, "BookTicket failed",
+            tag.Error(err), tag.UserID(req.UserID),
+            tag.TicketTypeID(req.TicketTypeID), tag.Quantity(req.Quantity),
+        )
+    }
     status, publicMsg := mapError(err)
```

One line of guard. Skip `log.Error` for expected business states (sold out), keep it for real system failures.

## Headline results

### Throughput + latency

| Metric | Run A (original) | Run B (skip sold-out log) | Δ |
|:--|--:|--:|--:|
| http_reqs/s | 46,361 | **59,374** | **+28.1%** |
| iterations/s | 46,361 | 59,374 | +28.1% |
| http_req_duration avg | 6.02 ms | 3.65 ms | -39.4% |
| **p50** | 4.45 ms | 2.79 ms | **-37.3%** |
| **p95** | 16.31 ms | 9.26 ms | **-43.2%** |
| **max** | 401.58 ms | 66.06 ms | **-83.6%** |
| Sold-out ratio | 99.69% | 99.69% | (same — same 50k pool) |

The tail latency improvement is the most striking: max wait drops from 400ms to 66ms — six times better worst-case experience.

### Block profile change

The block profile (where goroutines spend wall-clock time waiting, not where CPU burns) is the most diagnostic view here.

| Block source | Run A | Run B | Δ |
|:--|--:|--:|--:|
| **Total samples / 30s wall** | 3,354 s | **363 s** | **-89.2%** |
| `internal/poll.fdMutex.rwlock` (writeLock) | **2,828 s (84.3%)** | **0** (gone from top 10) | -100% |
| `runtime.selectgo` (channel wait) | 359 s | 305 s | -15% |
| `sync.Cond.Wait` | 99 s | 47 s | -53% |
| `runtime.chanrecv2` | 59 s | n/a | — |
| `sync.Mutex.Lock` | not in top | 11 s | introduced |
| HandleBook cumulative block | 2,882 s | 107 s | **-96.3%** |
| ├ log.Error chain | **2,818 s (97.8% of HandleBook)** | gone | -100% |
| └ service.BookTicket chain | 64 s | 107 s | (now dominant in handler) |

**Run A diagnostic**: 94 goroutines on average were blocked at any moment waiting for `fdMutex.writeLock` — the shared kernel-level FD lock on the booking_app process's stdout. Every Error-level log line is a `syscall.Write(stderr, ...)` that needs exclusive access to the FD; with ~46k requests/s × 99% sold-out × log.Error per request, the writeLock becomes the most contended primitive in the system.

**Run B diagnostic**: log.Error chain blocking is gone entirely. Remaining block time is dominated by `runtime.selectgo` (normal goroutine scheduling — `select` on channels) and small `sync.Cond.Wait` cases (likely go-redis pool connection wait).

## Key insight — why CPU profile alone was misleading

The first CPU profile showed `log.Error` consuming 28% of total CPU samples. I concluded "logging is the bottleneck". **This was a logically incomplete inference**: CPU% only tells you where CPU cycles are spent, not what limits throughput.

In this system:
- **booking_app CPU utilisation: 290% (2.9 cores)** out of 10 available — system was **CPU under-utilised by 71%**
- Freeing CPU on a non-CPU-bound system doesn't directly lift throughput
- The actual mechanism by which `log.Error` capped throughput is **lock contention on shared FD**, not CPU consumption

The block profile reveals this immediately:
- Run A: `fdMutex.rwlock` accounts for 2,828 / 3,354 = 84% of all goroutine blocking time
- After fix: that line disappears from the top 10

**Lesson**: when CPU is not saturated but throughput is capped, the answer is in the block / mutex profile, not the CPU profile. The user's pushback ("could there be other factors? lock contention? gin? nginx?") was the right scepticism.

## What this means for the architecture story

### Original blog post claim: "8,330 acc/s is the Lua single-thread ceiling"
**Status: invalidated three times over**:
1. Today's measurement: 46k req/s (Run A) → 59k req/s (Run B) on the same hardware
2. Redis baseline showed deduct.lua does 273k RPS server-side (32× higher than 8,330)
3. Block profile shows the cap was logging, not Lua

The 8,330 number was either:
- Pre-D7 architecture (when the saga ate `order.created` and added latency to the accepted path)
- A different test methodology
- Or simply stale

Either way, the framing "Redis Lua serialisation is the physical ceiling" was wrong.

### Real story
1. **Booking hot path is fast** — deduct.lua server-side is microseconds (~3.65µs/invocation per the Redis baseline measurement)
2. **The HTTP pipeline cap is set by whoever serialises goroutines** — in Run A, that was the kernel's per-FD writeLock; in Run B, it's distributed across Redis/PG client pools and runtime scheduler
3. **`log.Error` on a high-volume expected business state is an anti-pattern** — it conflates "system failure" with "inventory exhausted", and the structured-log write path goes through a single FD lock that becomes the system bottleneck

### Production implication
Demoting `ErrSoldOut` / `ErrTicketTypeSoldOut` from `log.Error` to a metric increment (or a sampled log) is a one-line change that:
- Adds 28% throughput to flash-sale capacity
- Halves p95 latency for the entire booking path (not just sold-out requests — happy path also benefits because they share the same FD lock)
- Reduces 83% of worst-case tail latency

## How to reproduce on different hardware

1. Apply the same one-line guard in `internal/infrastructure/api/booking/handler.go:170-187`
2. Run `make stress-k6 VUS=500 DURATION=90s` with mutex/block profile enabled (`runtime.SetMutexProfileFraction(1)` + `runtime.SetBlockProfileRate(1)` in `buildPprofServer`)
3. Capture profiles inside container via:
   ```bash
   docker exec booking_app sh -c '
     wget -qO /tmp/cpu.prof   "http://127.0.0.1:6060/debug/pprof/profile?seconds=30" &
     wget -qO /tmp/mutex.prof "http://127.0.0.1:6060/debug/pprof/mutex?seconds=30" &
     wget -qO /tmp/block.prof "http://127.0.0.1:6060/debug/pprof/block?seconds=30" &
     wait'
   ```
4. Compare `internal/poll.fdMutex.rwlock` in the block profile across A vs B

## Artifacts

- `A_k6.log` / `B_k6.log` — k6 full output (90s each, 500 VUs, 50k pool)
- `A_cpu.prof` / `B_cpu.prof` — 30s CPU profile during steady-state load
- `A_mutex.prof` / `B_mutex.prof` — 30s mutex contention profile
- `A_block.prof` / `B_block.prof` — 30s block profile

Analyse with `go tool pprof -top -nodecount=15 <prof>` or `go tool pprof -http=:8081 <prof>` for the flame graph view.

## Code state

All experimental changes have been **reverted** — current code on `feat/d4.1-kktix-ticket-type` is back to baseline (log.Error unconditional, mutex/block profile fractions at 0, config cross-check intact). The A/B fix is a recommendation for a future PR, not a committed change.
