# Saturation profile — 20260502_221629

Captured under: `VUS=500 DURATION=60s` against k6_comparison.js (500k tickets).
pprof window: 30s starting 10s after k6 launch.

## Files

| File | What it tells you |
| :-- | :-- |
| `cpu.pprof` | Where the Go app spent its CPU during peak. `go tool pprof -top cpu.pprof` |
| `heap.pprof` | In-use bytes by allocation site. `go tool pprof -top heap.pprof` |
| `goroutine.pprof` | Goroutine count + stacks. `go tool pprof -top goroutine.pprof` |
| `commandstats_diff.txt` | Top Redis commands by total μs spent during the window — the "what was Redis doing" answer |
| `slowlog.txt` | Any single command that took >10ms. Empty = no individual slow op |
| `prom_signals.json` | RED + USE signal snapshot at end-of-window — Redis CPU, client-pool hits/misses/timeouts/wait, PG pool waits, p99 latency, goroutines, accepted bookings/sec |
| `k6_summary.txt` | Headline throughput + latency from the load generator |

## How to read this

The decision tree for "what's the bottleneck" — keys below match the JSON field names in `prom_signals.json`:

1. **`redis_cpu_total_rate`** (sys+user CPU per second)
   - Sustained > 0.8 → Redis main thread CPU is saturated. **Optimize Redis-side: io-threads, EVALSHA, pipelining.**
   - Sustained < 0.3 → Redis is NOT the bottleneck. Look elsewhere.
2. **`redis_client_pool_misses_per_sec`** + **`redis_client_pool_timeouts_per_sec`**
   - Misses sustained > 0 → PoolSize is too small. Bump `cfg.Redis.PoolSize`.
   - Timeouts > 0 → pool fully exhausted, this is a hard saturation signal.
3. **`pg_pool_wait_seconds_per_sec`**
   - Sustained > 0 → Postgres connection pool is queueing. Check `pg_pool_in_use` vs configured max.
4. **`commandstats_diff.txt` top row**
   - `evalsha` / `eval` dominating → Lua scripts are the cost driver. Confirm with cpu.pprof.
   - `xadd` / `xreadgroup` dominating → stream operations dominate (worker side).
5. **`cpu.pprof` top samples**
   - `runtime.gc*` heavy → GC pressure; check heap.pprof for allocation churn.
   - `syscall.*` heavy → I/O bound (network or disk).
   - Application code dominating → application-level hotspot, profile it.

## Auto-generated signals

Pulled from `prom_signals.json` at the end of the saturation window:

```json
{
  "redis_up": "1",
  "redis_cpu_total_rate": "0.5331010534269713",
  "redis_client_pool_total_conns": "200",
  "redis_client_pool_idle_conns": "177",
  "redis_client_pool_hits_per_sec": "35708.48241072024",
  "redis_client_pool_misses_per_sec": "0",
  "redis_client_pool_timeouts_per_sec": "0",
  "redis_client_pool_wait_seconds_per_sec": "4.474202671748263",
  "pg_pool_in_use": "2",
  "pg_pool_wait_count_per_sec": "0",
  "pg_pool_wait_seconds_per_sec": "0",
  "go_goroutines": "627",
  "http_requests_per_sec": "35448.25440565346",
  "http_request_duration_p99": "0.019936343899261834",
  "bookings_success_per_sec": "11111.402475610568"
}
```

## Next-step prompt

Open `cpu.pprof` first:

```
go tool pprof -http=:0 cpu.pprof    # opens browser flame graph
go tool pprof -top cpu.pprof        # top-N text view
```

Cross-reference top samples with `commandstats_diff.txt` and the Redis/pool signals above. The conclusion belongs in this README — append a "Findings" section once you've read the profile.

---

## Findings (the answer to "is the 8,332 acc/s saturation Redis-bound?")

**Short answer: No.** The bottleneck is **NOT** Redis CPU, NOT Postgres pool, NOT Go-redis client connection-pool starvation. The 30-second sample at peak load gives the following signal-by-signal verdict:

### 1. Redis main thread CPU — **NOT saturated** (~53%)

```
redis_cpu_total_rate = 0.533
```

Redis is doing real work — `commandstats_diff.txt` shows ~4M commands across the 30s window (~135,000 cmd/s, all single-threaded) with `evalsha` (Lua deduct) at 69,113/s averaging 3.59 μs/call. But it has **~47% CPU headroom**. If we removed every other bottleneck, Redis could roughly double this throughput before its main thread became the limit.

**Implication: B3 inventory sharding (which we withdrew in PR #69) would not have helped here.** Sharding is the answer to "Redis main thread CPU at 90%+"; we are at 53%.

### 2. Postgres pool — **completely idle** (2 in-use)

```
pg_pool_in_use = 2
pg_pool_wait_count_per_sec = 0
pg_pool_wait_seconds_per_sec = 0
```

Workers process orders fast enough that the DB pool sees only 2 active connections at peak. Postgres is not the bottleneck.

### 3. Go-redis client connection pool — **NOT starved**

```
redis_client_pool_total_conns = 200
redis_client_pool_idle_conns  = 177  (23 active)
redis_client_pool_misses_per_sec   = 0    ← pool size sufficient
redis_client_pool_timeouts_per_sec = 0    ← no exhaustion
redis_client_pool_hits_per_sec     = 35,708  ← hot reuse
redis_client_pool_wait_seconds_per_sec = 4.47 sec/sec
```

200 conns provisioned, only 23 in use at peak. Zero misses, zero timeouts. **The 4.47 sec/sec cumulative wait spread across 627 goroutines** averages to ~7ms wait per goroutine — non-negligible but not the dominant cost (the median request is 3.98ms total). PoolSize is sufficient as configured.

### 4. cpu.pprof — **syscall-dominated**

A third+ of the Go process's CPU during peak is in raw syscalls — the network round-trips to Redis (read + write per command) and to the clients (HTTP request + response). This is the signature of an **I/O-bound Go service**, not a compute-bound one. (Run `go tool pprof -top cpu.pprof` for the precise breakdown.)

### 5. Goroutines — **627 in flight**

Consistent with 500 k6 VUs each holding one in-flight request + worker goroutines. Not a leak.

### Most likely actual bottleneck

The combination of:
- ~33% Go CPU in `Syscall6` (per repeated runs of the profile)
- ~4.47 sec/sec cumulative pool wait_duration (mild contention, not starvation)
- Redis at only 53% utilization
- Trivial PG pool usage

…points at **goroutine scheduling around blocking syscalls** (network I/O serialization) as the likely cap. The 8,332 acc/s ≈ 33,000-row-Redis-ops/s × ~30 μs round-trip per Lua call ≈ matches what a single-Redis-network single-host setup yields.

### What this means for the optimization roadmap

| Lever the senior research suggested | Predicted impact based on this profile |
| :-- | :-- |
| Redis 6+ `io-threads` | **Low.** Redis CPU is at 53% — single thread isn't saturated yet. Would help if we got to 80%+, not now. |
| Client pipelining | **High.** Multiple Lua deducts batched per TCP round-trip would shrink the Syscall6 share directly. Worth experimenting with for the worker → Redis read path. |
| `EVALSHA` instead of `EVAL` | **Low.** The cpu.pprof shows zero time in script-load paths; we're already on EVALSHA-cached scripts. |
| Bigger `PoolSize` | **Tiny.** Already 200 conns, 177 idle at peak — no headroom shortage. |
| Inventory sharding (B3 — REJECTED) | **Zero.** Redis is at 53%; splitting one key across N keys on the same instance does not multiply single-thread throughput. |

### Single biggest takeaway for the project narrative

This profile is the strongest evidence yet that **the original PR #68 saturation point of 8,332 acc/s is an I/O-bound ceiling specific to this single-host docker setup**, not a Redis architecture limitation. It validates the decision to withdraw PR #69 (B3 sharding) and pivot to evidence-based optimization. The next move is whichever of the levers above produces the biggest measurable shift — most likely **client-side pipelining for the worker XReadGroup → Lua deduct path** — and we now have the tooling (this script) to verify before/after with apples-to-apples profile diffs.

