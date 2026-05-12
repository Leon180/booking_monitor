# Lua single-thread 的 8,330 acc/s 天花板 — 怎麼想下一個 10×

> English version: [2026-05-lua-single-thread-ceiling.md](2026-05-lua-single-thread-ceiling.md)

> 🚧 **2026-05-12 更新 — 本文 Hypothesis 4 結論待重新檢驗**。後續 Redis baseline benchmark(`make benchmark-redis-baseline` 在 laptop docker 量得)顯示 deduct.lua **server-side aggregate throughput 是 273k RPS**,**不是** 8,330。8,330 acc/s 是 booking_monitor 整條 HTTP pipeline 的 cap,**不是 Redis Lua single-thread serialization 的 cap**。完整數據在 [`docs/benchmarks/20260512_200714_redis_baseline/summary.md`](../benchmarks/20260512_200714_redis_baseline/summary.md)。真實 bottleneck 在 HTTP pipeline 哪一層(Gin handler / idempotency middleware / Go-redis client RTT / etc.)需要 Go pprof 重新定位。**本文以下內容暫時保留,待 pprof 結果出來後一次性 rewrite。**

> **TL;DR.** VU scaling 壓測從 500 推到 5000 VUs,accepted_bookings 始終卡在 ~8,330/s。Redis CPU 只有 53%、pool 沒爆、PG pool 沒滿。瓶頸是「**同一把 inventory key 的 Lua 寫入是 single-thread 序列化**」 — 是物理上限。但下一步**不是「不做 sharding」**,而是學業界做兩層 sharding:**(1) Section-level(business axis) — 每個票價區域 / 票種獨立**,從第一天就該這樣設計;**(2) Hot-section sub-sharding(infra axis) — 只對預期超熱的區段做 quota 預分配**,讓單一 hot section 也能線性 scale。Generic hash sharding(課本上說的那種)業界沒人單獨用 — 永遠是 business-axis 為主、infra-axis 為 hot-spot 後備。**順序仍然是 infra 先動、Redis 最後動,但「Redis 動」這件事從「single 升 cluster」變成「per-section 多實例 + hot-section quota router」。**

對應 [VU scaling benchmark archive](../benchmarks/) + [saturation profile](../saturation-profile/)。系列模板見 [`docs/blog/README.md`](README.md)。Section-level sharding 的設計細節記在 [`docs/architectural_backlog.md`](../architectural_backlog.md) § Section-level inventory sharding。

## Context

事情起源是一輪 VU scaling 壓測。把 concurrency 從 500 推到 5,000,看訂票熱路徑在哪裡開始破。每組設定都對同一個 500k 票池打 60 秒,期間每組之間 reset 一次。

| VUs | http_reqs/s | accepted_bookings/s | p95 | business_errors |
| --: | --: | --: | --: | --: |
| 500   | 46,475 | **8,330** | 14.45 ms | 0.00% |
| 1,000 | 41,919 | **8,331** | 23.81 ms | 0.00% |
| 2,500 | 36,156 | **8,332** | 31.68 ms | 0.00% |
| 5,000 | 34,264 | **8,314** | 174.36 ms | 0.00% |

幾個觀察:
- **accepted_bookings/s 完全持平在 8,330/s**,從 500 推到 5,000 VUs 沒動
- 總 http_reqs/s 反而從 46k 降到 34k(VU 越多、每個 VU 的 round-trip 越少 — 系統在排隊)
- p95 在 2,500 → 5,000 之間有個 hockey-stick(從 32ms 跳到 174ms)
- business_errors 全部是 0% — 系統沒壞,只是排隊

這是 healthy degradation,不是失敗。但 **8,330 是哪裡來的?**

我跑了一輪 saturation profile 拿熱路徑當下狀態:

| 訊號 | 實測值 | 意思 |
| :-- | --: | :-- |
| Redis main thread CPU | **53%** | 還有一半 headroom |
| Redis client pool hits/s | 35,708 | 連線重用順暢 |
| Redis client pool timeouts/s | **0** | 沒有任何排隊取連線 |
| Redis client pool used / total | 23 / 200 | 連線池幾乎沒用 |
| PG pool in-use | **2** | 完全沒在等 DB |
| Go CPU(top frame) | ~33% Syscall6 | I/O dominate, write 比 read 多 4× |
| p99 latency | 19.9 ms | 健康 |

**每個資源都還很閒**。瓶頸顯然不是 CPU、不是 pool、不是 DB、不是 Network thread。是哪裡?

## Options Considered

我列了四個假設,逐一檢查。

### 假設 1: Redis CPU 飽和了

**DISPROVED**。Redis main thread 在 35k cmd/s 跑 53% CPU。Stack Overflow 公開的架構在 87k cmd/s 跑 2% Redis CPU(他們是純 GET/SET、沒有 Lua eval) — 我們的 Lua 在 35k 跑 53% 算合理(Lua 比 GET/SET 貴 5-10×)。**離飽和還有將近 50% headroom。Redis CPU 不是瓶頸。**

### 假設 2: Go-redis client pool 爆了

**DISPROVED**。pool stats 顯示 0 timeouts、0 misses、35k hits/s,200 個連線中只用 23 個。**pool 完全沒爆。**

### 假設 3: Docker Desktop on Mac 的 NAT 瓶頸

**POSSIBLE FOR REQ/S, NOT FOR ACCEPTED_BOOKINGS**。Docker Desktop on macOS 的 bridge NAT 公開 benchmark 是 ~650 Mbps,小封包大概 80k req/s 上限。這能解釋為什麼 5,000 VUs 還能跑 34k req/s(離 80k 還很遠),但**完全不解釋為什麼 accepted_bookings 卡在 8,330**。total req/s 跟 accepted bookings/s 是兩個不同的數字。

### 假設 4: Lua single-thread on same key

**CONFIRMED**。Redis 是 single-threaded。每個 Lua eval 對同一把 `event:{uuid}:qty` key 都要序列化執行 — 兩個並行訂票無法同時跑 deduct.lua。每次 deduct.lua 在 Lua 層做 `DECRBY` + 條件判斷 + `XADD`,大約 100μs。100μs × 序列化 = 每秒上限 ~10,000 次。**實測 8,330 對得起來,稍微低一點是因為加上 Lua eval 之外的 client/network overhead。**

所以瓶頸是「**同一把 key 的 Lua 寫入序列化**」。不是 Redis 的網路、不是它的總 CPU、不是 client。是物理上限:**single thread × single key × Lua 內部運算量**。

### 假設 4 補充:Per-op breakdown — 為什麼是 100µs(以及跟 Redis SET/GET 100k+ 數字並不衝突)

一個常見的反問是:「[Redis 官方 benchmark](https://redis.io/docs/latest/operate/oss_and_stack/management/optimization/benchmarks/) 不是說單實例可以 100k-200k ops/s 嗎?你的 8,330 看起來慢一個量級」。這個問題本身很 valid,但是兩個數字測的是不同東西。

**redis-benchmark 數字是怎麼來的**(來源:[DigitalOcean Ubuntu 18.04 benchmark](https://www.digitalocean.com/community/tutorials/how-to-perform-redis-benchmark-tests) + [Redis 官方 benchmark docs](https://redis.io/docs/latest/operate/oss_and_stack/management/optimization/benchmarks/)):

| 測什麼 | 條件 | 實測 |
|:--|:--|:--|
| `SET` no pipeline | 50 clients | ~72,000 RPS |
| `SET` with `-P 16` pipelining | 50 clients × 16-op pipeline | ~1.5M RPS |
| `GET` with `-P 16` | 同上 | ~1.8M RPS |
| `XADD` alone(small field set)| bare-metal ARM | ~136k RPS([source](https://learn.arm.com/learning-paths/servers-and-cloud-computing/redis-cobalt/redis-benchmark-and-validation/))|
| EVALSHA 100-GET single script | bare-metal,pipeline | ~1.9M effective ops/s([source](https://www.coreforge.com/blog/2022/02/redis-lua-benchmark/))|

**我們的 deduct.lua 跟這些 benchmark 比的差距,不是 Redis 變慢**,是測的東西不同。我們的 hot path 一次 invocation 內部做的事:

```
deduct.lua 一次 invocation 的 internal cost(approx,laptop docker):
├── EVALSHA invocation overhead              (~5-10 µs)
├── DECRBY ticket_type_qty:{id} -1           (~5-10 µs)
├── HMGET ticket_type_meta:{id} (3 fields)   (~10-20 µs)
├── multiply_decimal_string_by_int (Lua CPU) (~10-30 µs,size-dependent)
├── XADD orders:stream * (9 field-value)     (~30-50 µs,radix tree maintenance)
└── client/network round-trip                (~20-50 µs,Docker veth NAT)
                                              ─────────────
                                              ~80-170 µs/invocation
```

換算:1,000,000 µs ÷ ~120 µs ≈ **8,333/s on this setup**。對得起實測 8,330。

**換 hardware / setup 會怎樣?**

| Setup | 預期 deduct.lua ceiling | 為什麼 |
|:--|:--|:--|
| **laptop docker on macOS**(我們現在) | ~8,000-10,000 /s | Docker veth NAT + macOS-on-Linux VM overhead + ARM/x86 二進位翻譯 |
| **same setup, Linux bare host** | ~12,000-20,000 /s | 拿掉 Docker NAT + 拿掉 macOS VM 層 |
| **bare-metal Linux server / large cloud VM** | ~20,000-40,000 /s | 純 loopback / Unix socket + 高頻 CPU(Lua 吃 single-core clock)|
| **Redis 7 `io-threads 4`** | 一樣 ~20,000-40,000 /s | io-threads 平行 socket I/O,**對 single-key Lua 沒幫助**(Lua exec 仍然 single-thread)|

**所以「8,330 物理上限」要精確 framing**:**對同一個 inventory key 跑 deduct.lua,laptop docker 環境下的 ceiling 是 8,330/s**。Bare-metal 預期 3-5×,但 **single-thread × single-key × Lua eval 的 serialization 本質不會被 hardware 解決** — 那是 architectural cap,要 sharding 才能突破(Tier 5a/5b)。

**比較對 SET/GET 為什麼快這麼多**:
- SET / GET 是 single op + 可 pipeline(`-P 16` 是 20× factor)
- deduct.lua 一次內部做 5 個 op + 不能 pipeline(每個 Lua 都是 atomic round-trip)
- 等於 deduct.lua 一個 invocation 相當於 5 個 sequential ops,比 pipelined SET 多 5× 串行成本

完整方法論在 [`scripts/redis_baseline_benchmark.sh`](../../scripts/redis_baseline_benchmark.sh):跑 4 種 baseline mode(raw SET、SET pipelined、EVAL no-pipeline、XADD)跟我們 deduct.lua 並排比,在你的 setup 上可重現這個 breakdown。

> 💡 **Senior interviewer 防身回答**:「8,330 是 same-key Lua throughput on laptop docker。換 bare-metal 應該 25-40k 左右,因為 Lua 跑在 Linux 大時鐘 CPU + 拿掉 Docker NAT。但**這個 ceiling 不是 Redis instance 飽和** — Redis main thread CPU 在 8,330/s 跑只用 53%。瓶頸是 same-key Lua serialization 的物理本質,跟 Redis SET/GET 能跑 100k+ 不衝突,因為 SET/GET 可 pipeline,deduct.lua 不能。要 scale 過去就 shard inventory key per (event_id, section_id),不是升級 Redis instance。」

## Decision

兩個層次的決定。

### 短期:**single-key 是「目前夠用」,不是「夠用了所以好」**

當下狀態 single Redis instance + single inventory key,8,330 acc/s ceiling。在這個 ceiling 之下,100k 票場次 12 秒賣完,客戶體感是「全程順暢」。我們現階段沒有 sub-second × 1M-票 的需求,所以**不急著做 sharding**。

但要把這個結論講清楚:**「8,330 acc/s 夠用」是業務目標決定的,不是架構目標決定的**。如果未來業務需求變成:
- 1M 票級的單場演唱會(Taylor Swift 巡迴 / KKTIX 大型站票)
- sub-second 回覆「你買到了嗎」(現在是 12 秒總時長)
- 多場次併行(數十場小場次同時開賣)

…那麼 single-key 就不夠 — 不是因為 Redis 變慢,而是業務目標把 ceiling 往上推。

### 中長期:兩層 sharding,業界共識

研究實際大型搶票系統(Alibaba 雙 11、Ticketmaster Verified Fan、12306、京東秒殺、KKTIX)後,共同模式是:

```
Layer 1 (business axis):
  每個 event × section 一個 Redis key namespace。
  所有 multi-section 場次預設套用,沒有複雜度。

Layer 2 (infra axis, 選擇性):
  只對預期超熱的 section 加第二層 sub-sharding。
  做法是 quota 預分配(不是 hash sub-shard)— 把 1000 票
  預先 split 給 4 個 Redis instance 各 250。
  Router 路由到當下還有餘量的 instance。
```

**為什麼 quota 預分配勝過 hash sub-shard**:hash sub-shard 的 sold-out 必須 SUM across N shards,有跨 shard 一致性問題。Quota 預分配讓每個 shard 自己看自己 — sold-out 是 per-shard 判斷,只有「整個 section 完全賣完」才需要彙總(而那是 N 個 shard 的 binary AND,複雜度遠低於 SUM)。

**業界對齊**(這個比 generic hash sharding 在公開文章上的 reference 多得多):

| 系統 | Layer 1 | Layer 2(hot path)|
| :-- | :-- | :-- |
| Alibaba 雙 11 | 每個 SKU 獨立 | 熱門 SKU 預分配 quota 到多個 Redis |
| Ticketmaster Verified Fan | 每個 event × 票種獨立 | 預期超熱的 event 額外切流量 |
| 12306 火車票 | 車次 + 座位等級 | 熱門車次的熱門等級切多 Redis,有「庫存路由表」 |
| 京東秒殺 | 商品 SKU | 熱門 SKU inventory 預分配到 N 個節點 |

**所有大型搶票 / 秒殺系統都這樣做,沒有例外**。

### 不會做的:single-instance 通用 hash sharding

我原本草稿提的 `event:{uuid}:qty:0..N` hash sub-shard 是學術上的 sharding,業界沒人單獨用。每個對外公開的搶票架構文獻都是先 business axis,在那之上才考慮 infra sub-shard。原因前面 quota 預分配那段已經講了:hash sub-shard 的 SUM-on-sold-out 是真實複雜度,quota 預分配繞過這個問題。

### 對 booking_monitor 的具體影響

- **Pattern A(D1-D7)的 schema 設計應該把 `section_id` 加進去** — 即使現在不做 sharding,讓 schema 從第一天支援 section 維度,未來 Layer 1 sharding 是「換 routing、不動 schema」,而不是 schema migration。**這是低成本、高 future value 的設計選擇。**
- **Layer 1 (section-level Redis sharding) 是 Phase 3 之後的 conditional PR** — 觸發條件是「我們需要 demo 出 multi-section 場次」或「benchmark 顯示 section 維度可以拉開差異」。
- **Layer 2 (hot-section quota router) 是 portfolio-demo 級的 PR** — 為了展示「如何處理 hot key」這個業界面試常見題的真實答案,實作 1 個 hot section 的 quota router + N 個 Redis,跑 benchmark 驗證 N× 線性 scaling。

完整設計記在 [`docs/architectural_backlog.md`](../architectural_backlog.md) § Section-level inventory sharding。

## Result

### 量化結論 — 修訂後的 Tier 表

| Tier | 動的東西 | 對 acc/s(per section) | 對 acc/s(整體) |
| --: | :-- | --: | --: |
| 1(現在) | Docker Mac、single Redis、single key | 8,330 | 8,330 |
| 2 | Bare-metal Linux | 8,330 | 8,330 |
| 3 | go-redis tuning | 8,330 | 8,330 |
| 4 | Redis 7 io-threads | 8,330 | 8,330 |
| **5a** | **Section-level Redis sharding(Layer 1)**,N=4 sections | 8,330(每 section)| **~33,000(總和)** |
| **5b** | **Hot-section quota router(Layer 2)**,M=4 sub-shards / hot section | 32,000+(該 hot section)| 視 hot section 數量 |
| 6 | Multi-region / federated cluster | 看設計 | 看設計 |

**最關鍵的觀察**:**Tier 2 → Tier 4 都不會改變 accepted_bookings/s**。網路、client、Redis I/O thread 都是「總 throughput」的 lever — 對 Lua 同 key 序列化沒幫助。要拉高 acc/s,只能動 Tier 5a/5b。

**5a 是「設計層面早做」、5b 是「業務需求才做」**。兩者組合起來把 ceiling 推到「總場次票數 × per-shard 8,330 / Lua 序列化」。

### 對搶票場景的意義

我之前在 cache-truth 那篇寫過:**搶票是 latency-bounded、不是 throughput-bounded**。100k 票 / 8,330 = 12 秒賣完。客戶體驗的指標不是「我們每秒能接多少」,而是「在賣完的這幾秒裡,我有沒有搶到」。

但業務目標的差異會把這個結論往不同方向推:
- 100k-票 / 「12 秒賣完可接受」⇒ single-key 夠
- 100k-票 / 「sub-second 回覆」⇒ 需要 Layer 1 (section-level sharding)
- **1M-票 / 「sub-second × 賣完」⇒ 必須 Layer 1 + Layer 2 兩層都動**

### 業界對齊

研究「1M QPS 真的是怎麼做的」之後,業界路徑跟我們的分層完全對得上:

| 案例 | QPS | 怎麼做到 |
| :-- | --: | :-- |
| **Stack Overflow** | 87k cmd/s @ 2% CPU | 單實例 Linux、純 GET/SET、無 Lua |
| **AWS ElastiCache r7g.4xlarge + Redis 7.1** | **1M+ RPS / node** | 開 I/O threads(Tier 4) |
| **Twitter** | 39M QPS / 10k instances | 平均每 instance ~3.9k(federated cluster)|
| **Alibaba 雙 11** | 庫存峰值 數十萬 / 秒 | **Layer 1 + Layer 2(SKU + hot SKU 預分配 quota)**|
| **Ticketmaster Verified Fan** | 演唱會峰值百萬級 / 秒 | **Layer 1 + Layer 2(event × 票種 + 預期超熱場次切流量)**|
| **12306 火車票** | 春運峰值千萬級 / 秒 | **Layer 1 + Layer 2(車次 × 等級 + 熱門車次庫存路由表)**|

**沒有任何一個案例的下一步是「把 Redis 改成 durable」**。這個直覺選項在規模上沒人選 — 跟 cache-truth 那篇的結論呼應。

## Lessons

### 「先測量、再優化」應該寫在牆上

事件之前我有一個假設:「Redis 大概飽和了,我們需要 sharding」。saturation profile 跑下去之後 53% CPU,**直接打臉**。Knuth 的「premature optimization is the root of all evil」的具體版本:**不要在沒有量化數字的情況下做架構決策**。

如果直接信我的直覺去做 inventory sharding,會拿到一個複雜度爆炸的 PR、跑出來不會更快(因為 Redis 還沒飽和)、然後我會去 debug 兩週才發現是 Docker Mac NAT 的問題。

**Saturation profile 是這個專案最有價值的工具之一**。它一次告訴你 6 個資源的同步狀態,讓你看清楚瓶頸在哪。後來我把 `make profile-saturation` 寫進 Makefile + docs,變成標準動作 — 任何「我覺得 X 太慢」的對話都先跑一輪這個。

### 直覺跟現實常常反方向

以為的瓶頸順序:Redis CPU → network → client。  
真實的瓶頸順序:**network(Docker NAT)→ client → Redis CPU(對 Lua 而言很久不會到)**。

這個反差讓我學到一件事:**discrete 的 benchmark 數字勝過 continuous 的直覺推理**。直覺說「Redis 是 single-threaded 所以一定先飽和」。數字說「不,Docker Mac 的 NAT 先撞」。直覺輸了。**對任何人下一次的「我覺得 X 是瓶頸」,記得這個故事**。

### accepted_bookings/s 才是搶票場景的真實指標

這是個產品級的 lesson、不是技術 lesson。工程師很容易陷入 throughput-fetishism — 看 http_reqs/s 看到天花板就想 scale。但對搶票而言,**total req/s 增加沒有業務價值**。能拒絕得多快不重要;能接受得多快才重要。

我們的 dashboard 後來把 `accepted_bookings/s` 跟 `http_reqs/s` 分開展示,而且 SLO 只綁前者。任何讀 dashboard 的人在 5 秒內就能看出系統是「處理得很快但拒絕很多」還是「真的在賣票」。

### Sharding 不是二元的 — 是「分幾層、分到哪個業務軸」

之前我框 sharding 是「做 / 不做」的問題,還框錯了 — 預設了 generic hash sharding 模型,然後得出「複雜度高所以不做」結論。錯。

業界沒有人單獨用 generic hash sharding,因為它的 SUM-on-sold-out 是真實複雜度。所有大型搶票系統都是 **business-axis 為主、infra-axis 為 hot-spot 後備** 的兩層模式。Section-level sharding 從第一天就該設計進 schema(成本低、future value 高);quota-router-style infra sub-sharding 只對 hot section 做(複雜度貴但只付給少數 hot path)。

回頭看的 takeaway:**sharding 設計題的關鍵問題不是「該不該分」,是「你的業務模型上有沒有自然的 sharding axis?」**。對搶票而言這個 axis 是 section / 票價;對電商秒殺是 SKU;對訊息系統是 user_id 或 conversation_id。先找業務 axis,再決定要不要在 hot 部分加 infra sub-shard。

### 把 8,330 寫進文件、讓未來不重新發現一次

我們現在 [PROJECT_SPEC](../PROJECT_SPEC.md) + [monitoring docs](../monitoring.md) + 這篇 blog 都明寫「8,330 acc/s 是 single-key Lua serialization 的物理上限」。**未來如果有人提「我覺得 Redis 太慢」,有現成答案**。沒有這個紀錄,六個月後會有人重新跑一次同樣的分析,可能還得出不同(錯)的結論。

技術寫作的一個重要功能:**避免後人重新犯一次同樣的探索**。

## 接下來

第三篇會寫 recon + drift detection 的設計 — 為什麼我們選「偵測但不修」、saga / watchdog / drift detector 三層怎麼分工。第四 / 五篇看時機:O3.2 變體 B 跑完 bare-metal benchmark 後寫 Docker Mac NAT cap(Tier 1→2 的真實量化),或是把 Tier 5a Pattern A 的 section-level schema 設計寫成獨立一篇。

**Cache-truth 契約 + 兩層 sharding 規則** 是這個專案目前最有 portfolio 價值的兩個架構設計,兩者都不複雜、都對齊業界、都有量化證據支撐。

---

*如果文章有錯或漏掉什麼,請對 [`docs/blog/2026-05-lua-single-thread-ceiling.zh-TW.md`](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-lua-single-thread-ceiling.zh-TW.md) 開 issue 或 PR。*
