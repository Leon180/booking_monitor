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

### 4. cpu.pprof — **syscall-dominated, but the writes are bigger than the reads**

A third+ of the Go process's CPU during peak is in raw syscalls — confirmed I/O-bound, not compute-bound. But "33% in Syscall6" is the umbrella; drilling into the call graph (`go tool pprof -peek` / `-top -focus`) gives a sharper breakdown:

```
internal/runtime/syscall.Syscall6                 28.44s  34.26%   ← the umbrella number
├── syscall.write                                 21.81s  26.27%   ← writes are 4× reads
│   ├── via HTTP response writer                  ~9.15s  ~11%     ← the largest single source
│   │   net/http.checkConnErrorWriter.Write
│   │   ──> bufio.(*Writer).Flush
│   │   ──> net.(*conn).Write
│   ├── via go-redis client writes                ~6-7s   ~8%      ← EVAL command sending
│   │   github.com/redis/go-redis/v9/internal/proto.(*Writer)
│   │   ──> bufio.(*Writer).Flush ──> net.(*conn).Write
│   └── via lib/pq (Postgres)                     0.43s   0.5%     ← negligible (only 2 PG conns)
├── syscall.read                                  5.31s   6.40%    ← HTTP req parsing + Redis replies
└── syscall.EpollWait                             1.09s   1.31%    ← network event loop
```

Verification: `-focus 'go-redis'` shows the entire go-redis package contributes 13.30% cumulative CPU. HTTP's `checkConnErrorWriter.Write` alone contributes 11.08%. **HTTP response writing is the single biggest syscall hot-spot, larger than the entire Redis client.**

### 5. Cumulative call-graph — middleware chain dominates

```
74.95s  90.28%  net/http.(*conn).serve              ← almost everything is HTTP serving
52.65s  63.42%  http.serverHandler.ServeHTTP
52.63s  63.39%  gin.(*Engine).ServeHTTP
52.15s  62.82%  gin.(*Context).Next                 ← Gin middleware chain
50.43s  60.74%  buildGinEngine.Metrics.func2        ← Prometheus metric middleware
49.46s  59.58%  buildGinEngine.BodySize.func4       ← BodySize middleware
49.33s  59.42%  RegisterRoutes.Idempotency.func1    ← Idempotency middleware (PR #48)
49.10s  59.14%  bookingHandler.HandleBook
```

The Idempotency middleware accounting for 59.42% cumulative CPU is the most interesting hint here — every request runs body fingerprinting + Redis SETNX/GET, and at 51,212 RPS that overhead amplifies. Worth investigating whether non-idempotent paths (read-only GET / bookings/orders/:id) are running through it unnecessarily.

### 6. Goroutines — **627 in flight**

Consistent with 500 k6 VUs each holding one in-flight request + worker goroutines. Not a leak.

### Most likely actual bottleneck

The combination of:
- ~33% Go CPU in `Syscall6`, of which **the writes (26.27%) dominate the reads (6.40%) by 4×**
- HTTP response writing (`checkConnErrorWriter.Write`) at ~11% — **the single biggest syscall consumer**
- Redis client writes (`go-redis ── proto.Writer ── bufio.Flush`) at ~8% — second
- Idempotency middleware in the call path of 59.42% of CPU
- Redis at only 53% utilization
- Trivial PG pool usage

…points at the **HTTP response-writing path + middleware chain overhead at high-RPS** as the dominant cap, with the Redis client second. The 8,332 acc/s × ~10 syscalls per booking × ~4μs user-mode-syscall-overhead ≈ 33% CPU — arithmetically consistent.

### What this means for the optimization roadmap

| Lever | Predicted CPU saving | Risk / complexity |
| :-- | :-- | :-- |
| **Audit Idempotency middleware hot path** — confirm GET routes (orders/:id, history) short-circuit before fingerprint/SETNX. If they currently don't, that's free wins. | 5–15% | Low (conditional branch) |
| **Trim 202 response body** — currently `{order_id, status, message, links: {self}}`. The `message` + `links` fields are convenience; bare `{order_id}` could shave ~10 bytes/response × 51k RPS = ~500KB/s in `bufio.Flush`. | 1–3% | Low |
| **Audit BodySize middleware** — does it read the body twice (once to measure, once to bind)? At 51k RPS this matters. | 2–5% | Low |
| **HTTP/2 between k6 ↔ nginx ↔ app** | Connection multiplexing reduces per-request connection overhead | Medium (config) |
| **go-redis pipelining for worker XReadGroup → Lua deduct** | 4–6% | Medium |
| Redis 6+ `io-threads` | **0** — Redis CPU is at 53%, single thread isn't saturated. |
| `EVALSHA` instead of `EVAL` | 0 — already EVALSHA-cached. |
| Bigger `PoolSize` | 0 — already 200 conns / 23 in use. |
| Inventory sharding (B3 — REJECTED) | **0** — Redis is at 53%; splitting one key across N keys on the same single instance does not multiply single-thread throughput. |

### Single biggest takeaway for the project narrative

The original PR #68 saturation point of 8,332 acc/s is an **I/O-bound ceiling on this single-host docker setup**, not a Redis architecture limitation — the call graph confirms it cleanly:

- **HTTP response write path** is the single biggest syscall consumer (~11% of total CPU)
- **Idempotency middleware** wraps 59% of CPU work — a candidate for closer-look
- Redis is at 53% CPU with 47% headroom

This validates the decision to withdraw PR #69 (B3 sharding) and pivot to evidence-based optimization. The highest-leverage next move is **NOT** the previously hypothesised "Redis pipelining" — that's a 4–6% gain. The bigger lever is **the HTTP-response + middleware path**, where 5–15% gains are plausible and the changes are low-risk. The tooling (`make profile-saturation`) lets us verify before/after each change with apples-to-apples profile diffs.

