# 我以為是 Lua,結果不是 — 一場 6 輪 benchmark 偵探故事

> English version: [2026-05-wrong-five-times.md](2026-05-wrong-five-times.md)

> **TL;DR.** 我之前寫過一篇 [Lua single-thread 的 8,330 acc/s 天花板](2026-05-lua-single-thread-ceiling.zh-TW.md),結論是「同一把 inventory key 的 Lua serialization 是物理上限」。**這個結論錯了。** 跑了 5 輪不同角度的 benchmark + pprof + A/B + 開放型負載重新驗證後,真實 picture 是:(1) Redis-side Lua 真實 server-side throughput 是 **273k RPS**,32× 高於 8,330;(2) 我們先前 closed-loop 量到的 cap 不是 Lua,是 `log.Error` 因為 sold-out 在每筆 request 寫 stderr,goroutines 在 `internal/poll.fdMutex.writeLock` 上 contention;(3) A/B 改 1 行 condition 移掉 sold-out 的 Error log → throughput +28% / p95 -43% / tail latency -84%;(4) 但是 **這個 bottleneck 只在 closed-loop 加上去的 adversarial load 下才會 trigger** — 真實 flash sale 開放型 arrival rate 8k req/s × 500k 票 × 100k+ unique users 下,系統 p95 sub-millisecond / 0 個 5xx。**單機現在的真實 ceiling 是「在當前 portfolio 規模下完全 overspecced」**。

這篇是上一篇 [Lua single-thread 8,330 acc/s ceiling](2026-05-lua-single-thread-ceiling.zh-TW.md) 的 follow-up。如果你還沒讀那篇先去掃一下,以下假設你看過了。

## Round 1: 用 Redis-side baseline 直接證偽原假設

我原本相信的 8,330 acc/s「物理上限」是 saturation profile 推算出來的:Lua serialization 每筆 ~100µs × serialize = 1s ÷ 100µs ≈ 10,000 ops/s 上限,實測 8,330 對得起來。

問題:這 100µs 是**從 saturation profile 觀察到的 system-level latency** 推回去 server-side Lua 的猜測,**沒有實際隔離測試**。

我寫了 [`scripts/redis_baseline_benchmark.sh`](../../scripts/redis_baseline_benchmark.sh) — 直接用 `redis-benchmark` 在 Redis container 內 loopback 跑 6 個 mode,**繞過所有 HTTP / Go-redis / Docker NAT layer**,只測 Redis 內部 Lua 真實 throughput:

| Mode | 測什麼 | RPS |
|:--|:--|--:|
| A | SET no pipeline | 290,697 |
| B | SET `-P 16` pipelined | 3,030,303 |
| C | GET `-P 16` pipelined | 3,333,333 |
| D | XADD alone | 286,532 |
| E | EVAL single DECR | 289,017 |
| F | **真實 deduct.lua(我們 hot path 那條)** | **273,972** |

**273k RPS** for deduct.lua 在同一把 key serialize 執行。換算 per-Lua exec time = **3.65µs**(不是我估的 100µs)。

這直接證偽:「same-key Lua serialization 是 8,330 cap」根本不成立 — Redis side 可以做到 32× 高。8,330 一定卡在 **Redis 以上** 的某一層。

## Round 2: CPU profile 看似找到 hotspot,但結論還是錯

抓 30s CPU profile during k6 load,top function:

```
0.08s 0.099% 0.099% 75.73s 91.13%  net/http.(*conn).serve
0.22s  0.27% 49.50s 61.47%  bookingHandler.HandleBook
                  23.51s 28.13%  log.Error (inline)        ← 看似 hotspot
                  20.58s 24.83%  service.BookTicket (Redis Lua)
```

`log.Error` 占 28% CPU。我快速結論:「sold-out 每筆 request 都 log.Error → 28% CPU 浪費在 log expected business state → 改掉就會大幅提升 throughput」。

寫到 [`docs/benchmarks/20260512_200714_redis_baseline/pprof_findings.md`](../benchmarks/20260512_200714_redis_baseline/pprof_findings.md) 第一版:**「log.Error 是 bottleneck」**。

## Round 3: 使用者打槍 — CPU 沒飽和怎麼會是 bottleneck?

「但是這樣就可以確認瓶頸在這邊嗎?有沒有可能有其他因素會影響?nginx?gin?觀測指標寫錯?」

對。我跳結論了。CPU profile 顯示「log.Error 占多少 CPU」**不等於**「它 cap throughput」。要回答後者要看其他資源使用率:

| 層 | CPU 使用率 | 狀態 |
|:--|--:|:--|
| **booking_app**(10 core,no CPU limit) | **2.9 cores** | ❌ **NOT saturated**(只用 29%) |
| **Redis**(1 core single-thread) | 0.61 core | ❌ NOT saturated(61% 還有 39%) |
| PG DB | 0.01 core | ❌ 不在 hot path(worker async) |
| nginx | 0% | ❌ 不在 path(k6 → app 直連) |

**CPU 沒飽和狀態下,光是減少 CPU work 不會線性提升 throughput**。Bottleneck 一定不是 CPU 本身,是某個 lock contention / GC pause / I/O wait。

CPU profile **告訴我們 CPU 在哪燒,沒告訴我們 throughput 被誰卡死**。這兩個是不同的問題。

## Round 4: Block profile 才看到真正機制

打開 mutex + block profile rate(`runtime.SetMutexProfileFraction(1)` + `runtime.SetBlockProfileRate(1)`),抓 30s block profile during load:

```
2828.31s 84.32%  internal/poll.(*fdMutex).rwlock    ← bottleneck 在這
  358.58s 10.69%  runtime.selectgo
   99.20s  2.96%  sync.(*Cond).Wait
   59.07s  1.76%  runtime.chanrecv2
```

Total block sample = 3354s in 30s wall = **94 個 goroutines 平均同時 blocked 在 `fdMutex.writeLock`**。

Drill down HandleBook block chain:

```
2882.21s   HandleBook (cumulative block time)
  2818.04s  └── log.Error chain                ← 占 97.77%!
       64.16s └── service.BookTicket (Redis)   ← 占  2.23%
```

**`log.Error` 不是 CPU bottleneck,是 FD write lock contention bottleneck**。每筆 sold-out 都呼叫 `log.Error` → zap encoder serialize → `syscall.Write(stderr, ...)` → 共用 stderr FD 需要**獨占 writeLock**。46k req/s × 99% sold-out × 每筆 log.Error → **94 個 goroutine 平均同時搶 stderr 寫鎖**,等於 system 內有一個隱形的 single mutex serializing 所有 HTTP request。

這跟原 blog 的 Hypothesis 4 結論完全一樣諷刺:**「single thread serialize on contended primitive」**。只是不是 Redis Lua 的 same-key,而是 process 的 stderr FD writeLock。

## Round 4.5: A/B test 證實 — 改 1 行 +28%

改 [`internal/infrastructure/api/booking/handler.go:175`](../../internal/infrastructure/api/booking/handler.go) 加 1 個 condition:

```go
if !errors.Is(err, domain.ErrSoldOut) && !errors.Is(err, domain.ErrTicketTypeSoldOut) {
    log.Error(ctx, "BookTicket failed", ...)
}
```

對 expected business state(sold-out)skip log.Error,real failure 還是 log。重 build + 重跑同樣 90s k6 load:

| Metric | Run A 原始 | Run B 移 sold-out log | Δ |
|:--|--:|--:|--:|
| **http_reqs/s** | 46,361 | **59,374** | **+28.1%** |
| **p50 latency** | 4.45 ms | 2.79 ms | -37.3% |
| **p95 latency** | 16.31 ms | 9.26 ms | -43.2% |
| **max latency** | 401.58 ms | **66.06 ms** | **-83.6%** |
| Total block samples | 3,354s | 363s | -89.2% |
| `fdMutex.writeLock` 時間 | 2,828s | **掉出 top 10** | -100% |

證據鏈完整:
1. Redis baseline 證偽 「Lua cap」
2. Block profile 證實 log.Error → fdMutex 是 contention 源
3. A/B test 量化 fix 效果 — throughput +28%、p95 -43%、tail -84%

寫到 [`docs/benchmarks/20260512_204000_ab_test_soldout_log/summary.md`](../benchmarks/20260512_204000_ab_test_soldout_log/summary.md)。

我以為故事到這結束了。**但使用者又打槍。**

## Round 5: 「你 methodology 整套就錯了」— 業界根本不這樣 benchmark

「為什麼之前的 rps 只有 8000 多和現在差那麼多呀?」 + 「可以用專業的 agent 幫我完整評估一次 benchmark 了解真實瓶頸嗎?或是我們改用真實情境:1000-5000 user request 搶票?可以幫我研究真實業界如何測試?」

兩個問題。第二個更狠 — 它指出**我們前 4 輪 benchmark 的根本 methodology 就不真實**。

Spawn search-specialist agent 研究業界(Ticketmaster / Stripe / Shopify / Alibaba 雙 11 / 12306)真實怎麼 benchmark 搶票場景。整理出來的關鍵發現:

### Closed-loop VU benchmark 跟業界真實 flash sale 是兩件不同的事

我們前 4 輪用的是 k6 `constant-vus` — **closed model**:每個 VU 等 response 才發下一個 request。System 慢的時候 → VU 自然等 → load 自動降低。這個現象有名字叫 **coordinated omission**([ScyllaDB 2021](https://www.scylladb.com/2021/04/22/on-coordinated-omission/))。

真實 flash sale 不是這樣:user F5 不停按、retry 不停送、不管 server 多慢都 hammer。對應的 k6 executor 是 `ramping-arrival-rate` — **open model**:新 request 按固定 rate 來,**跟 server response 時間無關**。

### 業界 reference 數字(對齊我們做的事)

| 系統 | 真實峰值 | source |
|:--|:--|:--|
| Ticketmaster Eras Tour 2022 | 14M 並發 unique users / 3.5B 總 requests / 2.4M 票/天 / 15% failure | [ticketmaster.com](https://business.ticketmaster.com/press-release/taylor-swift-the-eras-tour-onsale-explained/) |
| Shopify BFCM 2025 | **8.15M RPS** edge / 489M reqs/min | [shopify.engineering](https://shopify.engineering/bfcm-readiness-2025) |
| Alibaba 雙11 2019 | 544k 訂單/sec / 140M DB queries/sec | [SCMP](https://www.scmp.com/tech/e-commerce/article/3038539/) |
| 12306 春運 2025 | 1k+ tickets/sec sustained | [ourchinastory.com](https://www.ourchinastory.com/en/13985/) |
| Stripe BFCM 2024 | 20k+ RPS payment(99.9999% uptime) | [engineeringenablement.substack.com](https://engineeringenablement.substack.com/p/designing-stripe-for-black-friday) |

這些 system 都是分散式 / 多 region / CDN edge。**單機 throughput 不是這個遊戲的關鍵**。

### 業界推薦的 k6 寫法

```javascript
export const options = {
  scenarios: {
    flash_sale: {
      executor: 'ramping-arrival-rate',  // ← OPEN model
      preAllocatedVUs: 10000,
      maxVUs: 50000,
      stages: [
        { duration: '15s',  target: 8000 },  // ramp to 8k req/s
        { duration: '105s', target: 8000 },  // sustain
        { duration: '15s',  target: 0 },     // ramp down
      ],
    },
  },
};

export default function() {
  const uid = uniqueUserID();       // ← 每 iteration 一個 unique user
  const r = http.post('/api/v1/book', body);
  // 不 loop,VU exit。真實 user retry only on 5xx
  if (r.status >= 500) retry();
}
```

3 個關鍵設計:
1. `ramping-arrival-rate`(open model)
2. 每 iteration 一個 unique user(不重用 persona)
3. Realistic retry 行為(5xx retry,409 sold-out abandon)

## Round 5 實測 — 用 realistic methodology 重來

寫 [`scripts/k6_flash_sale_realistic.js`](../../scripts/k6_flash_sale_realistic.js) 套用所有 best practice。設 3 個 scenarios 對應不同 business 規模:

| Scenario | 票池 | Target 到達率 | 持續 | 業界對標 |
|:--|--:|--:|:--|:--|
| SMALL | 5,000 | 500 req/s | 25s | 小型 concert / club night |
| MEDIUM | 50,000 | 2,500 req/s | 50s | 中型 concert / 地方 sports |
| LARGE | 500,000 | 8,000 req/s | 105s | 大型 arena tour(單場館) |

跑完(這次跑 baseline,**沒有套 sold-out log fix**,故意保留先前發現的 inefficiency):

| Metric | SMALL | MEDIUM | LARGE |
|:--|--:|--:|--:|
| 到達率(實際) | 428 req/s | 2,142 req/s | 7,110 req/s |
| **Accepted bookings(202)** | **5,000** | **50,000** | **500,000** |
| Sold-out responses(409) | 9,999 | 99,999 | 459,998 |
| **Failures(5xx)** | **0** | **0** | **0** |
| http_req_duration p50 | 353 µs | 256 µs | 203 µs |
| **http_req_duration p95** | **746 µs** | **597 µs** | **1.14 ms** |
| http_req_duration max | 21.75 ms | 78.09 ms | 146.58 ms |
| 用了多少 VUs | 1(of 1000) | 6(of 3000) | **871(of 10000)** |
| 總 wall clock | 35 s | 70 s | 135 s |

**關鍵發現 — 我先前找的所有「bottleneck」根本沒 trigger**:
- 全部 inventory 賣完(5k / 50k / 500k 全售出)
- **0 個 5xx failures**(共 1.1M+ requests)
- p95 sub-millisecond up to 8k req/s arrival
- LARGE max VU 用 871(out of 10k preallocated) — 系統還很閒

「log.Error → fdMutex.writeLock」這個我前面確認的 bottleneck **在真實 traffic 下完全不是問題**。為什麼?
- closed-loop 測試:500 VUs 在 50k 票池跑 90s → tickets 在 ~1-2s 賣完 → 後 88s 都在「post-depletion 99% 409 fast path」上 hammer system → log.Error 在 1-2 秒內塞滿 stderr → contention
- open arrival rate 測試:user 按 8k/s 來 → tickets 在 ~68s 賣完 → 後 ~50s tail 才有 sold-out 但 arrival rate 已經 ramp down → **stderr 從來不擁擠**

**簡單講:bottleneck 是被 closed-loop 自己創造的 artifact。真實 user 行為下根本不會發生。**

## Round 6: 「現在這樣就跑了三次的 I/O 很貴」— 也是 architectural finding

正當我以為 Round 5 已經是故事結尾,使用者又補一刀:

> 「Client POST /api/v1/book → Gin 6 個 middleware → BookingService → 1× Redis EVALSHA(idempotency check)+ 1× domain validation + 1× Redis EVALSHA deduct.lua + 1× Redis EVALSHA(idempotency set)→ Build response. 這邊和 Redis 互動太多次了吧?難道不能一次完成嗎?既然是原子性的?其中一個失敗就失敗了才對?現在這樣就跑了三次的 I/O 很貴.」

兩件事一次點出:
1. **每筆 booking 跑 3 個 Redis ops 太多**
2. **3 個 ops 不是 atomic**

實際 trace code 後我承認部分錯誤:

| 情境 | Redis ops 數 |
|:--|--:|
| **沒送 `Idempotency-Key` header(我們所有前 5 輪 benchmark)** | **1**(只有 deduct.lua) |
| **送 `Idempotency-Key`(production-shape 真實 client)** | **3**(middleware Get → deduct.lua → middleware Set) |

我前面所有 benchmark **沒測 production-shape**。7,110 req/s 是「沒 idempotency 的 booking」數字。**真實 production 帶 idempotency 應該慢得多**。

### 加 `WITH_IDEMPOTENCY=true` env var 重跑

改 `k6_flash_sale_realistic.js` 支援 `WITH_IDEMPOTENCY=true` 後跑 4 個 test:

| Scenario | Target arrival | Path A (無 idempotency) | Path B (有 idempotency) | Δ p95 |
|:--|--:|:--|:--|--:|
| SMALL | 500 req/s | 0 5xx,p95 746 µs | 0 5xx,p95 928 µs | **+24%** |
| MEDIUM | 2,500 req/s | 0 5xx,p95 597 µs | 0 5xx,p95 688 µs | **+15%** |

Light load 下 Path B latency 多 15-24%,**throughput 不變**(系統還有 headroom)。但 ceiling hunt 結果出乎意料:

### Path B ceiling hunt 拿到驚喜 — 不是 throughput cap,是 **memory cap**

Ladder 跑 2k → 5k → 10k → 15k → 20k req/s,結果(2 分鐘):

```
Stage 1-2 (2k-5k req/s):  smooth, 0 5xx
Stage 3   (10k req/s):    cache 開始填到 512MB Redis 上限
Stage 4   (15k req/s):    OOM cascade 開始,5xx 暴衝
Stage 5   (20k req/s):    完全 degradation
```

最終(2M+ requests):**66.93% 是 5xx failure**(1.34M failures)。Redis 狀態:
- `used_memory_human: 509.43M / 512.00M` 全滿
- **662,410 個 idempotency cache entries**

每個 idempotency entry 存**整個 HTTP response body**(`order_id` + `reserved_until` + `links.pay` + fingerprint)≈ 500 bytes,加 24h TTL → **~660k 個 entries 就把 512MB 撐爆**。撐爆後 deduct.lua 也跟著 OOM-reject(因為 noeviction policy + Redis 一旦滿就拒寫)。

### 真實 production 三層 cap 完整 picture

| Cap class | Bottleneck mechanism | 觸發條件 | Fix |
|:--|:--|:--|:--|
| Lua single-thread-on-same-key | original blog hypothesis | — | ❌ Round 1 證偽(273k server-side) |
| `log.Error` → stderr fdMutex.writeLock | closed-loop sustained 99% sold-out | — | ✅ One-line guard(Round 4) |
| **`log.Error` 在 realistic open-model 下不會 trigger** | open-model arrival rate ≤ 8k | — | ✅ Round 5 驗證 |
| **CPU/scheduler exhaustion** | Path A open-model | **~25-30k arrival rate cliff** | scale-out / 削 per-req work |
| **Redis memory(idempotency cache size)** | **Path B(realistic production)** | **~10-15k req/s for ~2 min(660k entries 撐爆 512MB)** | ❗ **architectural decision** |

### 我先前所有 7k/59k/8330 數字應該怎麼讀

**7,110 req/s 是「沒 idempotency 的 intake-only」上限**。真實 production-shape(帶 idempotency)on 同 hardware:
- 短時間 burst:p95 多 15-25%,throughput unchanged
- 持續 10-15k req/s × 2 分鐘以上:Redis OOM → cascade fail

**Production-shape sustained ceiling on this hardware ≈ 10k req/s**,不是 7k 不是 59k。前面所有數字都還沒測 production-shape。

### Trade-off 紀錄(寫進 `docs/architectural_backlog.md`)

3 個 Redis ops 看似浪費,但**是 architectural 取捨**:

| Trade-off | 現狀(3 ops 分離) | Atomic Lua(1 op) |
|:--|:--|:--|
| Redis I/O | 3× round trip | 1× round trip |
| Atomicity | ❌(中間 fail 留 race) | ✅ |
| Middleware 通用性 | ✅(任何 endpoint 通用) | ❌(每 endpoint 自寫 Lua) |
| Cache entry size | Go 側決定 | Lua 必須傳完整 body |
| Lower-level safety net | UUID v7 + DB UNIQUE + reconciler 收尾 | 仍需要 |

booking_monitor 現狀 **跟 Stripe 公開的 idempotency 設計同 pattern** — 接受 race window + lower-layer 收尾,trade-off 對齊。

**Memory cap 是 separate concern**。最便宜的 fix:idempotency cache 只存 fingerprint + status code + content pointer(~80 B),不存整個 body → 6× memory headroom。沒做是因為現在 portfolio scope 還沒到觸發點。

### 第 6 個 lesson — 「testing methodology」也分 layer

| Layer | 我們 Round 1-5 測 | Production 真實 | Δ |
|:--|:--|:--|:--|
| Redis 內部 Lua | ✅ baseline | ✅ 同 | none |
| HTTP intake-only | ✅(Path A) | client 沒送 Idempotency-Key 也是 | none |
| **HTTP with idempotency** | ❌ **沒測** | client 99% 送 | **production cap 沒驗證過** |
| 完整 happy path(/pay + webhook + saga) | ❌ 沒測 | client 100% 會走 | **end-to-end 沒驗證** |

我們前面所有「ceiling」數字都是 intake-only。**真實 production end-to-end ceiling 應該再低 2-3 倍**,因為:
- Idempotency middleware: × 2-3 cost factor
- Worker stream consume + PG INSERT: 又 × N factor(沒測)
- Payment intent + webhook + saga: 又 × N factor(沒測)

要拿真實 end-to-end ceiling 還要再跑 `k6_two_step_flow.js` 跟完整 happy path 模擬。**今天還沒做。**

## Lessons learned(我這次學到的 5 件事)

### 1. 「能量化的數字 > 自信的推理」— 但要量化對的東西

原 blog 自信推「Lua 100µs serialize × → 8,330」是基於 sound principles(single-thread, serialization)。**錯在沒實際隔離測量 Lua server-side throughput**。一個 `redis-benchmark -t evalsha` 30 秒就推翻所有推理。

### 2. CPU profile 不是萬靈丹

CPU profile 告訴你「CPU 燒在哪」,**不告訴你 throughput 被誰卡死**。CPU 沒飽和狀態下,blocking profile(`runtime.SetBlockProfileRate`)才是 diagnostic 工具。我用了 CPU profile 跳結論 → 被使用者點出 → 才知道要看 block profile。

### 3. 同 mechanism 不同 layer — 不要太快類比

我原本以為 bottleneck 是 Redis-side Lua-on-same-key serialize。真實是 app-process-side 跨 goroutines 在 stderr FD writeLock serialize。**兩個都是「single-thread × contended primitive」**,但完全不同 layer / 不同解法。我的腦袋自動類比成同個 pattern → 漏看實際 mechanism。

### 4. Methodology > 數字本身

`http_reqs/s` 在 closed-loop sustained test 達 59k,跟 `accepted_bookings/s` 在 open arrival rate 達 3,703,**兩個數字都是真的**,但講不同故事。「業界怎麼測」這個 meta 問題比「我的系統 throughput 多少」更重要 — 因為前者決定後者怎麼解讀。

### 5. Senior interviewer 不在乎你 "right",在乎你「發現自己 wrong 後怎麼恢復」

我這次 wrong 了 4 次:(1) 原 blog Lua cap、(2) CPU profile 結論、(3) sold-out log 改完以為解掉、(4) closed-loop methodology 整套不真實。每次 wrong 都是 user(或我自己的反思)點出來。真實 portfolio value 在這條 trajectory,不在最後那個數字。

## 對 booking_monitor 的真實 narrative 更新

**現在我會這樣對 senior interviewer 講**:

> 「booking_monitor 在 MacBook Docker single-instance 跑 realistic open-arrival-rate flash-sale load 到 8 k req/s,p95 sub-millisecond,0 個 5xx。我有 3 層 evidence 支撐:
>
> 1. Redis-side baseline 量到 deduct.lua 273k RPS server-side
> 2. CPU + block profile 證實在 closed-loop adversarial load 下 stderr FD contention 是 cap(改 1 行 +28%)
> 3. Open-arrival-rate realistic test 證實這個 cap 在真實業務 traffic 下不會 trigger
>
> 我前面寫過一篇 blog 結論錯了 — 以為 Redis Lua 是 cap。後續發現是 logging FD contention。再後續發現連 logging contention 都只在 closed-loop 才會出現。**這個專案 portfolio value 在 methodology rigor + 自我修正,不在 raw RPS 數字**。Eras Tour scale(50k+ RPS sustained / 14M concurrent users)是 distributed system 問題,單 instance Go process 完全不是這個 game。」

對齊 Cake / Yourator / LinkedIn 上「single 5 秒答案 + tier classification」的紀律,這個 narrative 應該歸 Tier 1(深度擁有 + 多 evidence)。

## 完整 artifacts 清單

對 reviewer 完整透明,所有實驗 data + scripts 都在 repo 內:

| 路徑 | 內容 |
|:--|:--|
| [`scripts/redis_baseline_benchmark.sh`](../../scripts/redis_baseline_benchmark.sh) | Round 1 — Redis-side baseline benchmark(`make benchmark-redis-baseline`) |
| [`docs/benchmarks/20260512_200714_redis_baseline/`](../benchmarks/20260512_200714_redis_baseline/) | Round 1 artifacts + Round 2-3 CPU pprof |
| [`docs/benchmarks/20260512_204000_ab_test_soldout_log/`](../benchmarks/20260512_204000_ab_test_soldout_log/) | Round 4 A/B test artifacts(CPU + mutex + block profile × 2) |
| [`scripts/k6_flash_sale_realistic.js`](../../scripts/k6_flash_sale_realistic.js) | Round 5 — open-model arrival-rate k6 script |
| [`docs/benchmarks/20260512_210000_flash_sale_realistic/`](../benchmarks/20260512_210000_flash_sale_realistic/) | Round 5 3 scenarios summary |
| 本文 | 5 rounds 偵探故事 |

---

*文章寫得不對的地方歡迎 issue / PR。我每個 round 都可能還有錯,senior reviewer 看到請點出來。*
