# Redis baseline vs deduct.lua benchmark

**Timestamp**: 20260512_200714
**Host**: macOS arm64 (Apple Silicon), Docker Desktop, Redis 7.4.8 in container
**Conditions**: in-container redis-benchmark, 100,000 requests × 50 clients, loopback (no host NAT).
**Persistence**: AOF + RDB disabled (see `deploy/redis/redis.conf`) — ephemeral cache pattern.

## Mode-by-mode RPS

| Mode | What's measured | RPS | vs deduct.lua |
|:--|:--|--:|--:|
| A | SET no pipeline                              | 290,697 | 1.1× |
| B | SET pipelined `-P 16`                        | 3,030,303 | 11.1× |
| C | GET pipelined `-P 16`                        | 3,333,333 | 12.2× |
| D | XADD alone (small field set)                 | 286,532 | 1.0× |
| E | EVAL no-pipeline (single DECR)               | 289,017 | 1.1× |
| F | **deduct.lua** (real hot path)               | **273,972** | 1× |

## 🚨 重大發現:8,330 acc/s 不是 Redis Lua 的 cap

[Original blog post](../../blog/2026-05-lua-single-thread-ceiling.zh-TW.md) 假設 4 結論:「Lua single-thread on same key serialization is CONFIRMED;ceiling ~10,000/s,實測 8,330」。

**本次 baseline benchmark 直接推翻這個結論**:

- Redis-server-side deduct.lua aggregate throughput on **same key**(`ticket_type_qty:baseline`)with 50 concurrent clients = **273,972 RPS**
- 這比 k6 booking benchmark 量到的 8,330 acc/s **高 33 倍**
- 換言之 Redis Lua 的 same-key serialization **不是** 8,330 的 cap

所以 booking_monitor 的 8,330 acc/s 一定卡在 Redis-Lua **以上** 的某一層 — HTTP pipeline / Go-redis client / Gin handler / 中間 middleware 處理。**需要 Go pprof + saturation profile 重新定位真實 bottleneck**。

## 各 mode 的意義 (after correction)

- **A vs B/C** — pipelining lift = 10.4× (290k → 3M)。比預期的 20× 略低但 OK,Docker NAT 的 latency 拉長了 pipelining benefit
- **D ≈ A ≈ E** — XADD、SET、單 op Lua throughput 都在 286-290k 範圍,顯示 single-thread Redis 對單一 op 處理速度都差不多(`~3.5µs / op`)
- **F vs E** — deduct.lua(5 ops + Lua math)跟 single-op Lua 差距只有 5%(273k vs 289k!)。**這完全不符合「100µs/Lua invocation」假設** — 實際是 ~3.65µs/invocation
- **F vs B** — 11× gap。deduct.lua 一次 atomic 等於 SET-pipelined 11 個 op 的 cost,這個比值是 deduct.lua 內部 op count 的下限

## 真實的 per-Lua server-side cost

```
deduct.lua 真實 in-container server-side cost(本次測量推算):
─────────────────────────────────────────────────
1,000,000 µs / 273,972 RPS ≈ 3.65 µs / invocation

換算 50 concurrent clients:
  per-client 看到的 latency = 50 × 3.65 µs ≈ 183 µs/invocation

→ Lua 在 Redis 內 single-thread serialize 是真的,但
   每次 serialize 處理只花 ~3.65 µs(不是 blog 原本說的 100µs)
```

**Blog 原本的 100µs 估算錯在哪**:
- 把 "client 看到的 RTT"(~200µs)誤認為 "server-side Lua exec time"(~3.6µs)
- 沒有實測,只 plausibility-估
- 結論「8,330 是 Lua serialization cap」因此錯誤

## What this means for the architecture story

1. **8,330 acc/s 仍然是真實的測量值**,但**它不是 Redis Lua 物理上限**,而是 booking_monitor **整條 HTTP pipeline** 的 throughput cap
2. **Sharding inventory key 不會直接提升 8,330** — 因為 Redis 不是 bottleneck
3. **真實的 bottleneck 需要新一輪 profile** — Go pprof during k6 load,看時間花在哪
4. **Sharding 設計仍然 sound**(future-proof + business axis correct),但 motivation 從 "Redis Lua cap" 變成 "horizontal scale 準備"
5. **這個發現本身是 portfolio value** — "我跑 baseline 推翻自己的 diagnosis,誠實寫進文件" 比堅持原 framing 更 senior-level

## Reproducing on different hardware

執行 `make benchmark-redis-baseline REQS=100000 CLIENTS=50` 在不同 hardware:
- Bare-metal Linux:預期 F 跟今天差不多(Redis 本身已經很快),但 A/B/C 可能更高
- 比較 F/E ratio(real script vs single-op Lua):跨 hardware 應該幾乎一致 → 證明 architectural property
- 比較 F/A ratio:跨 hardware 一致 → 證明 deduct.lua internal op count 的 cost

## Raw output

See [`raw_output.txt`](raw_output.txt) for full `redis-benchmark` output per mode plus Redis INFO snapshot.

## 下一步 action items

1. **Update blog post** (`docs/blog/2026-05-lua-single-thread-ceiling.{md,zh-TW.md}`) — mark Hypothesis 4 conclusion as **invalidated by 2026-05-12 baseline benchmark**;add new Hypothesis 5 為「實際 bottleneck 仍待確認」
2. **Run Go pprof during k6** — 在 booking_monitor 跑 k6 時用 `go tool pprof -http=:8080 http://localhost:6060/debug/pprof/profile` 看 CPU profile,找出 HTTP pipeline 真實 hotspot
3. **Consider running idempotency-middleware off** 一次 benchmark,看 HTTP throughput 上限變多少(目前每次 booking 至少 3-4 個 Redis ops:idempotency check + EVALSHA + idempotency set)
4. **Memorialize the finding** — 把這個「benchmark 推翻 diagnosis」過程寫成新 blog post(比原本 single-thread-ceiling 更有 portfolio value)
