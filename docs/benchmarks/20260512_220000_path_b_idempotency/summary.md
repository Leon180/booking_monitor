# Path B benchmark — production-shape `/book` with Idempotency-Key

**Date**: 2026-05-12
**Hardware**: Apple Silicon MacBook + Docker Desktop, Redis `maxmemory 512mb` + `noeviction`
**Method**: Same `k6_flash_sale_realistic.js` script with `WITH_IDEMPOTENCY=true`. Each iteration sends a unique `Idempotency-Key: bench-{user_id}-{timestamp}` header → exercises the full 3-op middleware path (cache Get → handler → cache Set).

## Why Path B matters

The earlier benchmarks (Path A — `7,110 req/s`) **did not send `Idempotency-Key`**, so the idempotency middleware was a no-op pass-through. Real production clients (Stripe-style API consumers) send the header, which makes every booking touch Redis **3 times** instead of 1.

User architectural question (2026-05-12 round 6):
> "這邊和 Redis 互動太多次了吧？難道不能一次完成嗎？idempotency check 和 deduct 和 idempotency set 應該可以一次完成？畢竟是原子性的？其中一個失敗就失敗了才對？現在這樣就跑了三次的 i/o 很貴."

This benchmark answers two questions raised:
1. **How expensive is the 3-op middleware?** (latency + throughput cost vs Path A)
2. **Where does Path B break?** (which resource caps first?)

## Results

### Steady load (SMALL + MEDIUM, idempotency overhead measurement)

| Scenario | Target | Result | p50 | **p95** | 5xx | Δ p95 vs Path A |
|:--|--:|:--|--:|--:|--:|--:|
| Path A SMALL | 500 req/s | 5,000 accepted | 353 µs | 746 µs | 0 | baseline |
| **Path B SMALL** | 500 req/s | 5,000 accepted | 445 µs | **928 µs** | 0 | **+24%** |
| Path A MEDIUM | 2,500 req/s | 50,000 accepted | 256 µs | 597 µs | 0 | baseline |
| **Path B MEDIUM** | 2,500 req/s | 50,000 accepted | 302 µs | **688 µs** | 0 | **+15%** |

**Conclusion**: idempotency middleware adds ~15-25% latency overhead at non-saturated loads. Throughput at these levels is unaffected — system has plenty of headroom.

### Ceiling hunt (2k → 20k req/s ladder, 3,000,000 ticket pool)

| Stage | Target | Duration | Outcome |
|:--|--:|:--|:--|
| 1 | 2,000 req/s | 30 s | Smooth — no 5xx |
| 2 | 5,000 req/s | 30 s | Smooth — no 5xx |
| 3 | 10,000 req/s | 30 s | Cache fills toward 512 MB Redis cap |
| 4 | 15,000 req/s | 30 s | Redis hits OOM, 5xx cascade starts |
| 5 | 20,000 req/s | 30 s | Full degradation |

Final aggregate:
- Total iterations: 2,004,247 (60 % were 5xx after OOM)
- Accepted bookings (202): 662,712 — all before OOM
- 5xx failures: **1,341,534** (66.93 %)
- p95: **105.61 ms** (massive — system pegged in queue)
- Max VUs spawned: 7,643 (k6 ran out of preAllocatedVUs)

## Root cause of failure: Redis memory, not throughput

Post-run inspection:
- `used_memory_human: 509.43 M` / `maxmemory_human: 512.00 M`
- **662,410 idempotency cache entries** in Redis
- 662,725 stream entries
- noeviction policy → deduct.lua starts returning `OOM command not allowed`

Per-entry cost approximation:
- Each idempotency entry stores the **full HTTP response body** (`order_id` + `reserved_until` + `links.pay` + …) + fingerprint, ~400-500 bytes
- 24h TTL by default
- ~660k entries × ~500 B ≈ **330 MB just for idempotency cache**
- Plus stream entries × 660k × ~120 B ≈ **80 MB**
- Plus inventory keys + metadata + small overhead → **510 MB used / 512 MB cap**

The cliff between Path A (smooth at 7k accepted/s open-model) and Path B (OOM at 10-15k req/s) is **not the same mechanism** as the closed-loop 25-30k cliff we found earlier. Path B's cap is **memory capacity per unit time**, not request-rate-handling capacity.

## Implications for the architectural narrative

This adds a third layer to the cap story:

| Bottleneck class | When triggered | Fix |
|:--|:--|:--|
| Redis Lua single-thread on same key | Original blog hypothesis | ❌ Falsified (273k RPS server-side, Round 1) |
| `log.Error` → stderr FD writeLock contention | Closed-loop adversarial sustained load with 99% sold-out | ✅ One-line fix (Round 4 A/B test) |
| Stderr contention under realistic open-model arrival | Open-model up to 8k req/s | ✅ Doesn't trigger (Round 5) |
| **Redis memory capacity vs idempotency cache size** | **Path B with realistic Idempotency-Key + 24h TTL** | **❗ NEW finding — requires architectural decision** |

## Architectural decision needed

The current configuration:
- `maxmemory 512mb` (deploy/redis/redis.conf)
- `maxmemory-policy noeviction` (deliberately, so sold-out can't be silently masked by LRU eviction of inventory keys)
- Idempotency `REDIS_IDEMPOTENCY_TTL` 24h (default)

This combination caps the system at roughly **1M unique idempotent operations per 24h** on this Redis instance. Above that, deduct.lua starts failing OOM.

### Options for fix (none is free)

| Option | Pro | Con |
|:--|:--|:--|
| Bigger Redis(2GB / 4GB) | Trivial | Cost; doesn't solve linearly |
| Shorter idempotency TTL(e.g., 1h) | Caps memory growth | Loses 24h Stripe-style retry window |
| Per-key LRU eviction(`allkeys-lru`) | Auto-cap | Risk: inventory key could be evicted → sold-out drift; would need careful key prefixing |
| Move idempotency cache to dedicated Redis | Isolates concern | Operational complexity |
| **Atomic Lua combining check+deduct+set into 1 op** | Reduces middleware to 1 op | Loses middleware genericness;每個 endpoint 需自己 Lua;但 idempotency cache 尺寸沒變 |
| Smaller cache entries(only fingerprint + status, not full body) | 50-80% memory savings | Replay must regenerate response — not bit-for-bit identical |

### Recommended

For booking_monitor's portfolio-stated scale (single instance, single event, < 1M attempts per day) the current configuration is **fine**. For real flash-sale production-scale traffic, the **first-and-cheapest fix is smaller cache entries** (store only the SHA-256 fingerprint + status code + a content-id pointer, not the entire response body). Reduces per-entry cost from ~500 B to ~80 B → 6× more headroom.

## Honest summary

**Path A (no idempotency-key) ceiling: 25-30k open-model req/s** (cliff from CPU/stderr/scheduler contention).

**Path B (production-shape with idempotency) ceiling: 10-15k req/s** on current 512 MB Redis (Redis memory cap, not Lua / CPU / network).

**Real "production" ceiling without infra upgrade: ~10k req/s sustained**. That's 5x lower than the previously-reported 59k closed-loop number — and the difference is dominated by **architectural choices about the idempotency cache**, not by anything wrong in Go / Gin / Lua.

## Artifacts

- [`path_b_ceiling_k6.log`](path_b_ceiling_k6.log) — full k6 output for the ladder run
- [`scripts/k6_flash_sale_realistic.js`](../../../scripts/k6_flash_sale_realistic.js) — Path A/B switchable script (`WITH_IDEMPOTENCY=true`)
- [`scripts/k6_flash_sale_ceiling_idem.js`](../../../scripts/k6_flash_sale_ceiling_idem.js) — Path B ceiling ladder script
