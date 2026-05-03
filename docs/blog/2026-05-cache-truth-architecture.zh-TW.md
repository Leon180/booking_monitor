# 為什麼 Redis 是 ephemeral 而不是 durable — 1000 筆訊息中靜默遺失 411 筆,促使我們做的 cache-truth 重構

> English version: [2026-05-cache-truth-architecture.md](2026-05-cache-truth-architecture.md)

> **TL;DR.** 一次 smoke test 期間執行 FLUSHALL 導致 in-flight 的 1000 筆訊息中遺失了 411 筆 — 而我們完全沒察覺。修復方式不是把 Redis 改成 durable,而是把「**Redis 為 ephemeral、Postgres 為事實來源、漂移會被偵測並命名**」明確寫成架構契約,然後依照這個契約建立 rehydrate / NOGROUP-self-heal 告警 / 漂移偵測這幾層。五個 PR、約六週、~1300 LOC。可量化結果:漂移偵測器確認多次壓測後沒有非預期的漂移;consumer-group 重建 counter 維持為 0。更深層的成果:過去說不清楚的「Redis 在這裡到底要做什麼?」現在有一個一致的答案。

對應 [v0.4.0 release](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0)。系列 template 見 [`docs/blog/README.md`](README.md)。

## Context

事件當下我們的非同步 pipeline 結構:

```
POST /book → API → Redis Lua (DECRBY + XADD)
                 ↓                     ↓
                 (HTTP 202)            orders:stream
                                       ↓
                                       Worker (XReadGroup)
                                       ↓
                                       Postgres (INSERT order + outbox)
                                       ↓
                                       Kafka → payment service / saga compensator
```

熱路徑運作正常。C500 benchmark 穩定在 8,330 accepted bookings / 秒。Lua deduct + XADD 是原子的;consumer group 透過 PEL recovery 維持 exactly-once-ish processing。Phase 2 reliability sprint(CP1-CP9)落地了 runbooks、Alertmanager、多 process metrics、整合測試套件。**用我當時思考的所有指標來看,系統都在好的狀態。**

然後我們重跑了一次 smoke test,流程包含在壓測之間執行 `make reset-db`。reset 路徑會呼叫 `FLUSHALL` 來清 Redis。下一輪測試產生了 1000 個訂票請求,**全部回 HTTP 202**,但 worker 只處理了 589 筆。**剩下的 411 筆消失得無聲無息** — 沒有 DLQ entry、沒有 error log、沒有 metric 增加。客戶端被告知訂票成功;訂單從未進入 Postgres。

這不是 worker 的 bug。worker 是健康的。那 411 筆訊息**不是「處理失敗後遺失」,而是「從未被遞送給 worker」**。它們在 FLUSHALL 之前就被 XADD 進 stream,然後 FLUSHALL 把整個 stream 刪掉,接著 worker 的 XReadGroup 收到 `NOGROUP`,worker 的 NOGROUP 自癒機制透過 `XGROUP CREATE ... $` 重建 consumer group,而 `$` 這個參數明確的意思是「**從目前 stream 的尾端開始 — 跳過這個時間點之前的所有東西**」。所以 411 筆訊息被跳過,**不是「我們嘗試遞送但失敗」這種損失,而是更危險的「我們從未嘗試遞送、而且系統裡沒有任何東西注意到」**。

讓我睡不著的就是這個「靜默」。我們對 worker 端每一種失敗模式都有告警:XAck 失敗、解析錯誤、retry budget 耗盡、DLQ 寫入、saga 補償失敗、recon mark 錯誤。**唯一一個沒有告警的失敗模式,正是「consumer group 消失了,而重建跳過了我們已經接受的客戶訂單」**。

Redis 在這裡到底是要做什麼?當我們坐下來重新表述架構並仔細回答這個問題時,我們意識到答案**從一開始就是不一致的**。411 筆訊息的遺失不是實作上的 bug。是架構本身不一致,而實作忠實地執行了每個 commit 當下最方便的解讀。

## Options Considered

三條認真考慮過的路徑,加一條為了完整性而提的非選項。

### Option A — 把 Redis 改成 durable

開啟 AOF(append-only file)+ RDB(snapshot),讓 Redis 變成自己的事實來源,可以撐過 crash 跟 FLUSHALL。論點是:如果 Redis 丟資料,下游沒有東西能重建;所以 Redis 應該要 durable。

權衡:

- **Lua deduct 的延遲會明顯惡化** — 每次 XADD 都要打到 AOF 的 fsync。Stack Overflow 公開的 Redis benchmark 顯示 `appendfsync everysec` 大約 30-50% 吞吐量下降,`appendfsync always` 則達到 80%。我們會放棄 8,330 acc/s 這個門面數字,換來一個架構層級的宣稱(「Redis 是 durable 的」),但這個宣稱沒有任何我們真正會用到的運維結果。
- **NOGROUP recovery 還是壞的**。AOF replay 在 Redis 啟動時會還原 stream,但**手動的 `FLUSHALL`(就是這次的觸發點)會繞過 AOF**。`XGROUP DESTROY` 也是。一個 operator 開了 `DEBUG SLEEP` 太久,而另一個 tab 跑了 `FLUSHALL` 也是。Durability 解的是 crash,不是 operator 觸發的 state-loss。
- **把事實來源切成兩半**:Redis(現在主張 durability)和 Postgres(仍是我們的紀錄資料庫)。**兩個事實來源在 edge case 上會不同調 — 這就是每個 senior 事故報告開頭都是「然後我們發現資料庫和快取對某某情況的版本不一樣...」的標準配方。**

這是個真實的選項,真實的系統會挑這條路。但對我們的系統不對。

### Option B — Redis 跟 DB 之間用分散式鎖

用 Redlock(或 Redis 的 transactional 變體)讓「Redis 扣減完成」跟「Postgres commit 完成」變成原子。論點:如果它們不能不一致,漂移就不會發生。

權衡:

- **熱路徑上多一個協調者**(鎖服務)。每次訂票多至少一次 round-trip 去取鎖、做事、放鎖。熱路徑延遲上升;原本「靠 Lua 序列化做負載卸載」的特性,會變成「靠鎖爭用做負載卸載」,推理難度更高。
- **完全沒解到 FLUSHALL 這個 case**。鎖避免的是同時寫入時的不一致。它**不防止「operator 把快取清掉,所以快取現在說一件事、DB 說另一件事」**。那是另一種失敗模式。
- **Redlock 用作主要正確性機制(而非效能優化)時,有[眾所周知的正確性爭議](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html)**。

我考慮這條路大概一個小時就放下了。鎖的討論是個分心的話題;我們沒有並行 bug,而是有「**哪個 store 是 authoritative**」的架構模糊。

### Option C — Redis ephemeral、Postgres truth、補上中間層

明確定義契約:

> **Redis 是 ephemeral。Postgres 是事實來源。漂移會被偵測並命名。**

然後建立這個契約所需的層:
- 一個啟動時的 rehydrate,從 Postgres 重建 Redis,讓全新的 / 被 FLUSH 過的 Redis 在沒有 operator 介入下也能收斂到正確
- 一個對 consumer-group 重建事件的告警,讓靜默遺失訊息的 case 在發生當下就 surface 出來,**不是幾分鐘到幾小時後才從下游影響反推回來**
- 一個漂移偵測器,週期性 sweep 比對快取 vs 事實來源,讓任何 state divergence 都是可觀測且**被命名**的(不只是數字 — 是用**失敗模式方向**標記的 label)

權衡:

- **比 Option A 多寫程式碼**。Rehydrate、告警接線、漂移偵測器、runbooks、3 個新 metric。五個 PR 大約 1300 LOC。
- **不阻止遺失,只讓遺失變得可見**。Rehydrate 路徑落地後,未來再發生 FLUSHALL 還是會丟掉 FLUSHALL 當下到 worker 下一個 tick 之間的訊息 — 但**告警會在 60 秒內觸發,operator 會知道**。
- **運維上更乾淨**:跟 Alibaba 描述的搶票 stack(Redis 熱路徑、MySQL truth、async 對帳)以及 Stripe blog 對庫存儲存的描述(「durable store 是庫存的紀錄,不是 cache」)對得上。**當專案的架構選擇匹配公開的業界 pattern 時,每一個「為什麼這樣做?」的面試問題都變成 citation + trade-off 討論,而不是辯護。**

### Option Z — 「就不要跑 FLUSHALL」

運維上很誘人,智識上是個 fail。從第一原理審視的問題是「**系統在看到不該看到的 state 時要做什麼?**」。回答「operator 不應該讓我們進入那個 state」就是架構在告自己的狀。

## Decision

選 Option C,在六週裡分五個 PR 完成。**契約被明確寫進 [`docs/architectural_backlog.md`](../architectural_backlog.md) § Cache-truth architecture,在任何程式碼落地之前**,所以每一個 PR 的 reviewer 跟未來的我都有一個定義可以拿來測試 commit。

| PR | 解決的問題 | Scope |
| :-- | :-- | :-- |
| [#73](https://github.com/Leon180/booking_monitor/pull/73)(PR-A) | reset 路徑就是觸發點 | `make reset-db` 從 FLUSHALL 改成精確的 DEL on cache keys;reset 之間保留 Redis stream + consumer group,於是測試 reset 不再是 NOGROUP 觸發點 |
| [#74](https://github.com/Leon180/booking_monitor/pull/74)(PR-B) | 從乾淨的 Redis 復原 | App 啟動時的 `RehydrateInventory` — SETNX-not-SET、advisory-lock 序列化跨多 instance 啟動;明確的 `appendonly no` + `save ""` Redis 設定,把 ephemeral by design 寫白 |
| [#75](https://github.com/Leon180/booking_monitor/pull/75)(PR-C) | 偵測到實際發生的事件 | `consumer_group_recreated_total` counter + `ConsumerGroupRecreated` critical 告警(無 soak window — 一次發生就 page)。配 runbook,讓靜默遺失訊息的 case 是可偵測的而不是被埋掉 |
| [#76](https://github.com/Leon180/booking_monitor/pull/76)(PR-D) | 把漂移命名 | `InventoryDriftDetector` 跟 reconciler 共駐。每 60 秒比對 Redis 快取數量 vs Postgres `events.available_tickets`;三個 direction label(`cache_missing` / `cache_high` / `cache_low_excess`)導向不同的 runbook 分支 |
| [#77](https://github.com/Leon180/booking_monitor/pull/77) | Outbox-relay 的對應問題 | `outbox_pending_count` gauge,使用相同的「ephemeral cache vs durable source-of-truth」框架,只是換成 Postgres-outbox → Kafka 這個 bridge。**DB 失敗時送 0(不是 stale)**,讓積壓告警在 DB 離線期間不會用舊資料觸發 |

兩個橫切的硬化在 PR-D 的 fixup commits 落地:

- 三個 sweeper(Reconciler、InventoryDriftDetector、SagaWatchdog)的 sweep goroutine panic 復原機制,透過 `safeSweep` helper + `runtime/debug.Stack()` 捕捉 + `sweep_goroutine_panics_total{sweeper}` counter + `SweepGoroutinePanic` critical 告警。**關掉了「loop 死了但 process 還活著、/metrics 持續送 stale gauge」這個靜默失敗形狀** — 兩個 reviewer agent 各自獨立提出的 finding。
- recovery log 中的 stack trace 捕捉是第二輪 review 的 HIGH finding — `fmt.Errorf("panic: %v", rec)` 只 format panic value,**沒有 stack**。runbook 一直宣稱 operator 可以「看到 panic value + stack trace」,而實作只有 log value。`debug.Stack()` 現在在 deferred recover 內部呼叫,goroutine 還在 panic 中的 frame 時就抓下來。

## Result

### 量化結果

| 訊號 | 之前 | 之後 |
| :-- | :-- | :-- |
| Smoke-test reset 路徑 | `FLUSHALL`(摧毀 stream + group) | 精確的 `DEL event:* idempotency:* saga:reverted:*`(保留 stream + group) |
| `consumer_group_recreated_total` | 沒人看 | 第一次發生就告警,無 soak window |
| 啟動 rehydrate | 沒有 | 在 HTTP listener 啟動之前,以 fx OnStart hook 跑;失敗中斷啟動 |
| 漂移偵測 | 沒有 | 每 60 秒 sweep;三個 direction label 區分失敗模式 |
| Outbox 積壓 | 只有 `redis_stream_length`(錯的 stream) | `outbox_pending_count` + warning 告警在 >100 持續 5m |
| Sweeper goroutine panic | 靜默(loop 死掉、/metrics 持續送舊資料) | recover + counter + critical 告警 + log 中有 stack trace |
| Reset 後的漂移(smoke test) | 不知道 — 沒有偵測器 | 多次測試確認為 0 |

### 雙向的副作用

**原本期待的勝利**:FLUSHALL 事件的整條路徑現在端到端可觀察。**原本造成 411 筆訊息遺失的 smoke test**,現在會在 60 秒內觸發 `ConsumerGroupRecreated` 告警;runbook 告訴 operator 交叉比對 `bookings_total` 跟 DB orders 數量,漂移偵測器確認復原後沒有殘留漂移。**原本的失敗模式仍然可能發生,但不能再是靜默的了。**

**原本沒預期到的勝利**:把契約寫下來讓未來每一個架構決策都變得更容易。當 recon process 為了 PR-D 的漂移偵測需要 Redis access 時,「這應該由獨立的 Redis instance 提供嗎?」這個問題只需要一句話的答案:不用,Redis 是 ephemeral 的,所以單一 instance 結構上是 OK 的 — durability 不是我們要 Redis 提供的屬性。當 outbox 積壓 gauge 需要決定 stale-vs-zero-on-error 語義時(#77),契約說「快取讀到 0 代表我們看不到、真相訊號是 errors counter」 — 跟 inventory drift gauge 同樣形狀。**契約把三個獨立的設計決策變成一條規則的三次套用**。

**接受的權衡**:更多程式碼。五個 PR、三個新告警、兩個新 runbook section、一個新 sweep loop、~1300 LOC。雙語監控文件(EN + zh-TW)隨每個 PR 鎖步成長。如果我們選 Option A(讓 Redis durable),diff 大概是十行程式碼加一個 config 變更。**明確性的成本顯示在 commit 數量上**。

### 業界對齊

落地後,我請 research agent 把我們的契約對應到業界實踐,並找看有沒有任何跟我們矛盾的東西。結論:每一個架構選擇都回 **STANDARD**,只有一個例外:

| 選擇 | 結論 | 來源 |
| :-- | :-- | :-- |
| Postgres 為事實來源 + Redis ephemeral 熱路徑 | STANDARD | [Alibaba 搶票架構](https://www.alibabacloud.com/blog/system-stability-assurance-for-large-scale-flash-sales_596968) 直接說明:庫存在 Redis,持久化資料庫為 authoritative。[Stripe 庫存 pattern](https://stripe.dev/blog/how-do-i-store-inventory-data-in-my-stripe-application) 一樣 |
| `appendonly no` + `save ""`(純 ephemeral Redis) | STANDARD | [AWS ElastiCache caching patterns](https://docs.aws.amazon.com/whitepapers/latest/database-caching-strategies-using-redis/caching-patterns.html) 直接支援。Memcached 的設計原則一樣 |
| 啟動 rehydrate 用 SETNX(保留 in-flight 值) | NOVEL but sound | 沒有公開 reference 專門寫 SETNX-not-SET,但底層邏輯 — 不要覆蓋進行中的值 — 是對的 |
| Lua atomic deduct(DECRBY + XADD) | STANDARD | Alibaba 直接點名:「LUA scripts 的 transaction 特性」用於「先讀剩餘庫存後扣減」。[Games24x7 case study](https://medium.com/@Games24x7Tech/running-atomicity-consistency-use-case-at-scale-using-lua-scripts-in-redis-372ebc23b58e) 證明可以 scale |
| 週期性漂移對帳 | STANDARD(公開資料少) | [SPIFFE issue #4973](https://github.com/spiffe/spire/issues/4973) 明確建議「週期性的 full-db reconciliation of cache」。普遍實踐;但少有公開寫文章 |
| 沒有分散式鎖(一致性靠 Lua + outbox) | STANDARD | [Redis 官方 guidance](https://redis.io/glossary/redis-lock/) 明確不建議用 SETNX-based locks 作為主要機制,而要用 fault-tolerant pattern;對我們來說,Lua atomicity + transactional outbox 已經涵蓋一致性需求,不需要 locking |

**唯一一個 NOVEL 的項目是 SETNX-on-rehydrate 這個 refinement**。它是標準 pattern 的合理 refinement,不是 anti-pattern。我有信心在面試問答中為它辯護。

這次研究最讓我意外的發現:**週期性漂移對帳這個 pattern 是「標準但很少公開記錄」**。每個 senior 從業者私下問都會同意「當然你會對帳 cache vs truth」這是顯而易見的;但幾乎沒有 engineering blog 把它寫出來。SPIFFE 的 GitHub issue 是最接近公開 reference 的。這代表:(a)我們在做這件事是處在好的群體中,但也代表(b)**我們在記錄 PR-D 漂移偵測器跟 runbook 時,寫出了這個 pattern 比較公開的 reference 之一**。

## Lessons

### 411 筆訊息的遺失不是 bug — 是架構本身不一致

實作忠實地執行了「Redis 在這裡做什麼」這個問題在每個 commit 當下最方便的解讀。沒有壞掉的 assertion、沒有傳統意義上漏掉的 error handler;**每一行程式碼都在做它的作者意圖的事**。bug 住在實作之間的縫隙裡:deduct 路徑的作者、worker 的 NOGROUP 自癒、用 FLUSHALL 的 reset script、saga compensator 的作者,**每個人對「哪個 store 是 authoritative」的心智模型都不太一樣,而且這些心智模型沒有任何一個被寫下來**。

修復不是加 error handling。**修復是把契約寫下來,然後改實作對齊**。契約現在在 `docs/architectural_backlog.md`,diff history 有 receipt。未來的我在 review 未來的 PR 時,有定義可以拿來測試。

### 「快取當作 durable storage」這個 anti-pattern 很狡猾

**如果你在事件發生前問我我們的系統有沒有把 Redis 當 durable,我會回答沒有**。我們有 Postgres。我們有 outbox。我們有 saga。我們顯然知道 Redis 是 ephemeral。然後我去 code 裡找這個假設,發現 worker 的 NOGROUP 自癒寫著「從 `$` 重建 group」 — **這只在「我們從現在開始會收到的訊息已經足夠」的前提下才合理。而這個前提只在 Redis 是 durable 時才成立。但它不是**。在我們已經口頭說過不是問題的那個地方,程式碼不一致到正中靶心。

回頭看的味道,我會盯的訊號:**任何從快取遺失中復原但沒有做相當於「從事實來源 rehydrate」的程式碼,要嘛你在主張一個你沒有的 durability,要嘛你在丟資料**。我們的 NOGROUP 自癒是後者;直到丟了 411 筆訊息我們才注意到。

### 漂移偵測是 observability,不是 auto-correction

PR-D 的 `InventoryDriftDetector` **不會自動修正**。它偵測漂移、命名失敗模式、page operator。三個不同的 reviewer 都問了某個版本的「這不是應該也修掉它找到的漂移嗎?」答案是不:

- **Auto-correction 跟 in-flight Lua deducts 競爭**。當客戶用 Lua 在 decrement 同一個 key 時,從 sweep loop 寫到 Redis key,正是原本架構設計要避開的多寫者 race。
- **沒有 operator visibility 的 auto-correction 會藏住底層失敗**。如果 saga 補償在 production 出 desync,「漂移偵測器靜默修掉它」會讓 bug 活幾個月。「漂移偵測器以 `direction=cache_high` page operator」會在幾分鐘內把 bug surface 出來。
- **復原動作 — 從 DB truth 重跑 rehydrate — 破壞性夠強(會碰 live 快取)** 應該由 operator gate,不該自主執行。

**保守預設 — 偵測 + 命名 + 告警,不要修 — 是 production observability 應該長的樣子**。「Self-healing」是個行銷詞;在分散式系統中,大多數 self-healing 是**靜默地壓掉症狀**。

### 我會做不一樣的事

三件事,以我有多少信心排序:

**1. 在第一個 PR 之前就寫契約文件**。我是在 PR-A 期間才寫契約,當時我已經開始寫 code 了。**它是專案中單一個槓桿率最高的寫作**;早一點做會讓 PR 序列更乾淨、review 討論更短。這是我會教自己未來十年的 Gall's law 變體:**一個架構決策如果無法用兩段話寫下來,大概還沒被做出來**。

**2. 把雙語文件契約當成架構的可執行 spec**。「變更實際落地了嗎?」這個問題,最有價值的測試居然是雙語的 `docs/monitoring.md` + `docs/monitoring.zh-TW.md` 更新。如果一個 metric 或 alert 沒在兩邊都記錄,PostToolUse hook 會抓到;如果 prose 模糊,**翻譯 EN 版本到 zh-TW 的動作會逼它變精確**。**雙語契約不是文件衛生 — 它是明確思考的 forcing function**。

**3. 在 PR-D 的 review cycle 早一點跑 multi-agent review**。reviewer 抓到的兩個 HIGH(不安全的 `errors.Is(err, ctx.Err())` pattern + `GetInventory` 的 `(0, nil)` 歧義)都微妙到我會 ship 它們。修復很小(~30 LOC,跨兩個 commit)。**成本是 PR 落地延遲一天;勝利是避免兩個潛在 bug 形狀的 finding 在一個月後打到一個無關的 PR**。我現在把「multi-agent review 有沒有說 HIGH?」當成 merge gate,不是「nice-to-have」 — 對任何碰到 resilience surface 的 PR 都是。

## 接下來

這是計畫中五篇系列的第一篇([`docs/blog/README.md`](README.md))。第二篇會涵蓋 Lua single-thread 在 8,330 acc/s 的天花板,以及怎麼想下一個 10× — 對應 [VU 壓測 benchmark archive](../benchmarks/) 跟我們在 Phase 2 做的 1M QPS 分析。

**Cache-truth 契約在 Phase 3 設計工作中持續產生紅利**。當 Pattern A 預訂流程需要把訂單狀態機擴展到 `awaiting_payment`,「預訂 TTL 的事實來源是什麼?」這個問題只需要一句話:Postgres `orders.reserved_until` 是事實來源,Redis 端的反映(如果有)遵循 cache-truth 契約 — 它們之間的漂移是可偵測且被命名的,不是被魔法修掉。**v0.4.0 的工作把一類沒解的問題變成一條我們持續套用的規則**。

---

*如果文章中有任何錯誤或漏掉的地方,請對 [`docs/blog/2026-05-cache-truth-architecture.zh-TW.md`](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-cache-truth-architecture.zh-TW.md) 開 issue 或 PR。**Blog 放在 in-repo 就是為了讓修正走跟程式碼一樣的 review 流程。***
