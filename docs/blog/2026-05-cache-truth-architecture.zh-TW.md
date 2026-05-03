# 為什麼 Redis 是 ephemeral 而不是 durable — 1000 筆訊息中悄悄掉了 411 筆,逼出來的 cache-truth 重構

> English version: [2026-05-cache-truth-architecture.md](2026-05-cache-truth-architecture.md)

> **TL;DR.** 一次 smoke test 跑了 `FLUSHALL`,讓 in-flight 的 1000 筆訊息掉了 411 筆 — 而我們完全沒察覺。修法不是把 Redis 變 durable,而是把「**Redis 是 ephemeral、Postgres 是 source of truth、漂移會被偵測並命名**」這條契約寫白,然後依契約補上 rehydrate / NOGROUP-self-heal 告警 / 漂移偵測這幾層。五個 PR、約六週、~1300 LOC。可量化結果:漂移偵測器在多次壓測後都確認沒有漂移殘留;consumer-group 重建 counter 維持為 0。更深的收穫:過去說不清楚的「Redis 在這裡到底是要做什麼?」現在有一個一致的答案。

對應 [v0.4.0 release](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0)。系列模板見 [`docs/blog/README.md`](README.md)。

## Context

事件發生時,我們的非同步 pipeline 長這樣:

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

熱路徑運作正常。C500 benchmark 穩穩跑在 8,330 accepted bookings / 秒。Lua deduct + XADD 是原子的;consumer group 靠 PEL recovery 維持 exactly-once-ish 處理。Phase 2 reliability sprint(CP1-CP9)該交的都交了:runbooks、Alertmanager、多 process metrics、整合測試套件。**用我當時思考事情的所有指標來看,系統都在好的狀態。**

然後我們重跑了一次 smoke test,流程包含在壓測之間執行 `make reset-db`。reset 路徑會呼叫 `FLUSHALL` 來清 Redis。下一輪測試打了 1000 個訂票請求,**全部回 HTTP 202**,但 worker 只處理了 589 筆。**剩下的 411 筆悄悄不見了** — 沒有 DLQ entry、沒有 error log、沒有 metric 跳。客戶端被告知訂票成功;訂單從來沒進到 Postgres。

這不是 worker 的 bug。worker 是健康的。那 411 筆訊息**不是「處理失敗後遺失」,而是「根本沒被送到 worker 過」**。它們在 FLUSHALL 之前就被 XADD 進 stream,然後 FLUSHALL 把整個 stream 砍掉,接著 worker 的 XReadGroup 收到 `NOGROUP`,worker 的 NOGROUP 自癒機制透過 `XGROUP CREATE ... $` 重建 consumer group — 而 `$` 這個參數的明確意思是「**從現在 stream 的尾端開始 — 之前的全部跳過**」。所以那 411 筆訊息被跳過,**不是「我們嘗試遞送但失敗」這種損失,而是更危險的「我們連嘗試遞送都沒嘗試,而且系統裡沒有任何東西注意到」**。

讓我難以入睡的就是那個「沒人發現」。對 worker 端的每一種失敗模式我們都接了告警:XAck 失敗、解析錯誤、retry budget 用完、DLQ 寫入、saga 補償失敗、recon mark 錯誤。**唯一一個沒有告警的失敗模式,正是「consumer group 不見了,而重建跳過了我們已經接受的客戶訂單」**。

Redis 在這裡到底要做什麼?當我們坐下來重新表述架構,認真回答這個問題時,才發現答案**從一開始就是不一致的**。411 筆訊息的遺失不是實作上的 bug,而是架構本身不一致 — 而實作只是忠實地執行了每個 commit 當下最順手的詮釋。

## Options Considered

三條認真考慮過的路,加一條為了完整列出而提的非選項。

### Option A — 把 Redis 變 durable

打開 AOF(append-only file)+ RDB(snapshot),讓 Redis 變成自己的 source of truth,撐得過 crash 跟 FLUSHALL。論點是:Redis 一旦丟資料,下游沒辦法重建;所以 Redis 應該要 durable。

取捨:

- **Lua deduct 的延遲會明顯惡化** — 每次 XADD 都要打到 AOF 的 fsync。Stack Overflow 公開的 Redis benchmark 顯示 `appendfsync everysec` 大約掉 30-50% 吞吐量,`appendfsync always` 則達到 80%。我們會犧牲 8,330 acc/s 這個漂亮數字,換來一個架構層級的宣稱(「Redis 是 durable 的」),但這個宣稱沒帶來任何我們真的會用到的運維結果。
- **NOGROUP recovery 還是壞的**。AOF replay 在 Redis 啟動時會還原 stream,但**手動的 `FLUSHALL`(就是這次的觸發點)會繞過 AOF**。`XGROUP DESTROY` 也是。一個 operator 開了 `DEBUG SLEEP` 太久、另一個 tab 跑 `FLUSHALL` 也是。Durability 解的是 crash,不是 operator 觸發的 state-loss。
- **把 source of truth 切成兩半**:Redis(現在主張 durability)和 Postgres(還是我們的紀錄資料庫)。**兩個來源在 edge case 上會不一致 — 那就是每篇 senior 事故報告開頭的標準台詞:「然後我們發現資料庫和快取對某情況的版本不一樣...」**

這是個真實選項,真實的系統會挑這條路。但對我們的系統不對。

### Option B — Redis 跟 DB 之間用分散式鎖

用 Redlock(或 Redis 的 transactional 變體)讓「Redis 扣減完成」跟「Postgres commit 完成」變成原子。論點:它們不能不一致,漂移就不會發生。

取捨:

- **熱路徑上多一個協調者**(鎖服務)。每次訂票多至少一次 round-trip 去取鎖、做事、放鎖。熱路徑延遲上升;原本「靠 Lua 序列化做負載卸載」這個性質,會變成「靠鎖爭用做負載卸載」,推理起來更複雜。
- **完全沒解到 FLUSHALL 這個 case**。鎖避免的是同時寫入時的不一致,**不防止「operator 把快取清掉、現在快取說一件事、DB 說另一件事」**。那是另一種失敗模式。
- **Redlock 用作主要正確性機制(而不是效能優化)時,有[眾所周知的正確性爭議](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html)**。

我這條路想了大約一小時就放下。鎖的討論只是分散注意力;我們沒有並行 bug,而是有「**哪個 store 是 authoritative**」的架構模糊。

### Option C — Redis ephemeral、Postgres truth、把中間層補起來

明文寫下契約:

> **Redis 是 ephemeral。Postgres 是 source of truth。漂移會被偵測並命名。**

然後建立這個契約所要求的層:
- 啟動時的 rehydrate,從 Postgres 重建 Redis,讓全新的 / 被 FLUSH 過的 Redis 在沒有 operator 介入下也能收斂到正確
- 對 consumer-group 重建事件的告警,讓「悄悄遺失訊息」這個 case 在發生當下就被看到,**不是幾分鐘到幾小時後才從下游影響推回來**
- 一個漂移偵測器,週期性 sweep 比對快取 vs source of truth,讓任何 state 分歧都是可觀測且**有名字的**(不只是數字 — 是用**失敗模式方向**標記的 label)

取捨:

- **比 Option A 多寫程式碼**。Rehydrate、告警接線、漂移偵測器、runbooks、3 個新 metric。五個 PR,大約 1300 LOC。
- **不阻止遺失,只讓遺失看得到**。Rehydrate 路徑落地後,未來再發生 FLUSHALL 還是會丟掉 FLUSHALL 當下到 worker 下一個 tick 之間的訊息 — 但**告警會在 60 秒內觸發,operator 會知道**。
- **運維上更乾淨**:跟 Alibaba 描述的搶票 stack(Redis 熱路徑、MySQL truth、async 對帳)以及 Stripe blog 對庫存儲存的描述(「durable store 是庫存的紀錄,不是 cache」)對得上。**當專案的架構選擇匹配公開的業界 pattern,每一個「為什麼這樣做?」的問題都變成 citation + 取捨討論,而不是辯護。**

### Option Z — 「不要跑 FLUSHALL 就好了啊」

運維上很誘人,智識上是個 fail。從根本原則審視的問題是「**系統在看到不該看到的 state 時要做什麼?**」。回答「operator 不應該讓我們進入那個 state」就是架構在告自己的狀。

## Decision

選 Option C,在六週裡分五個 PR 完成。**契約在任何程式碼落地前就先明文寫進 [`docs/architectural_backlog.md`](../architectural_backlog.md) § Cache-truth architecture**,讓每個 PR 的 reviewer 跟未來的我都有一個明確定義能拿來檢驗 commit。

| PR | 解決的問題 | Scope |
| :-- | :-- | :-- |
| [#73](https://github.com/Leon180/booking_monitor/pull/73)(PR-A) | reset 路徑就是觸發點 | `make reset-db` 從 FLUSHALL 改成精確的 DEL on cache keys;reset 之間保留 Redis stream + consumer group,測試 reset 不再是 NOGROUP 觸發點 |
| [#74](https://github.com/Leon180/booking_monitor/pull/74)(PR-B) | 從乾淨的 Redis 復原 | App 啟動時的 `RehydrateInventory` — SETNX-not-SET、advisory-lock 序列化跨多 instance 啟動;明確的 `appendonly no` + `save ""` Redis 設定,把 ephemeral by design 寫白 |
| [#75](https://github.com/Leon180/booking_monitor/pull/75)(PR-C) | 偵測到實際發生的事件 | `consumer_group_recreated_total` counter + `ConsumerGroupRecreated` critical 告警(無 soak window — 一次發生就 page)。配 runbook,讓「悄悄遺失訊息」這個 case 是可偵測的而不是被埋掉 |
| [#76](https://github.com/Leon180/booking_monitor/pull/76)(PR-D) | 把漂移命名 | `InventoryDriftDetector` 跟 reconciler 共駐。每 60 秒比對 Redis 快取數量 vs Postgres `events.available_tickets`;三個 direction label(`cache_missing` / `cache_high` / `cache_low_excess`)導向不同的 runbook 分支 |
| [#77](https://github.com/Leon180/booking_monitor/pull/77) | Outbox-relay 的對應問題 | `outbox_pending_count` gauge,套同一套「ephemeral cache vs durable source-of-truth」框架,只是換成 Postgres-outbox → Kafka 這個 bridge。**DB 失敗時送 0(而不是讓舊值留著)**,讓積壓告警在 DB 離線期間不會被舊資料誤觸發 |

兩個橫切的硬化在 PR-D 的 fixup commits 落地:

- 三個 sweeper(Reconciler、InventoryDriftDetector、SagaWatchdog)的 sweep goroutine panic 復原機制,透過 `safeSweep` helper + `runtime/debug.Stack()` 捕捉 + `sweep_goroutine_panics_total{sweeper}` counter + `SweepGoroutinePanic` critical 告警。**關掉了「loop 死了但 process 還活著、/metrics 還在送舊 gauge」這個沒人發現的失敗形狀** — 兩個 reviewer agent 都各自獨立提出這個 finding。
- recovery log 的 stack trace 捕捉是第二輪 review 的 HIGH finding — `fmt.Errorf("panic: %v", rec)` 只 format panic value,**沒帶 stack**。runbook 寫著 operator 可以「看到 panic value + stack trace」,而實作只 log value。`debug.Stack()` 現在在 deferred recover 內部呼叫,goroutine 還在 panic 中的那個 frame 時就抓下來。

## Result

### 量化結果

| 訊號 | 之前 | 之後 |
| :-- | :-- | :-- |
| Smoke-test reset 路徑 | `FLUSHALL`(連 stream + group 一起砍) | 精確的 `DEL event:* idempotency:* saga:reverted:*`(保留 stream + group) |
| `consumer_group_recreated_total` | 沒人在看 | 第一次發生就告警,無 soak window |
| 啟動 rehydrate | 沒有 | 在 HTTP listener 啟動之前,以 fx OnStart hook 跑;失敗就中斷啟動 |
| 漂移偵測 | 沒有 | 每 60 秒 sweep;三個 direction label 區分失敗模式 |
| Outbox 積壓 | 只有 `redis_stream_length`(看錯 stream 了) | `outbox_pending_count` + warning 告警在 >100 持續 5m |
| Sweeper goroutine panic | 沒人發現(loop 死掉、/metrics 還在送舊資料) | recover + counter + critical 告警 + log 帶 stack trace |
| Reset 後的漂移(smoke test) | 不知道 — 沒有偵測器 | 多次測試確認為 0 |

### 雙向的副作用

**原本就期待的收穫**:FLUSHALL 事件的整條路徑現在端到端可觀察。**原本造成 411 筆訊息遺失的 smoke test**,現在會在 60 秒內觸發 `ConsumerGroupRecreated` 告警;runbook 告訴 operator 交叉比對 `bookings_total` 跟 DB orders 數量,漂移偵測器確認復原後沒有漂移殘留。**原本的失敗模式還是可能發生,但不會再悄悄發生了**。

**沒預期到的收穫**:把契約寫下來讓未來每個架構決策都變得更容易。當 recon process 為了 PR-D 的漂移偵測需要 Redis access 時,「這應該由獨立的 Redis instance 提供嗎?」這個問題,只需要一句話就能回答:不用,Redis 是 ephemeral 的,所以單一 instance 結構上就 OK — durability 不是我們要 Redis 提供的屬性。當 outbox 積壓 gauge 需要決定 stale-vs-zero-on-error 語義時(#77),契約說「快取讀到 0 代表我們看不到、可靠的訊號是 errors counter」 — 跟 inventory drift gauge 同樣形狀。**契約把三個原本獨立的設計決策變成同一條規則的三次套用**。

**接受的取捨**:更多程式碼。五個 PR、三個新告警、兩個新 runbook section、一個新 sweep loop、~1300 LOC。雙語監控文件(EN + zh-TW)隨著每個 PR 同步成長。如果我們選 Option A(讓 Redis durable),diff 大概是十行程式碼加一個 config 變更。**「把話說清楚」的代價,寫在 commit 數量上**。

### 業界對齊

落地之後,我請 research agent 把我們的契約對應到業界實踐,順便看有沒有任何跟我們矛盾的東西。結論:每個架構選擇都回 **STANDARD**,只有一個例外:

| 選擇 | 結論 | 來源 |
| :-- | :-- | :-- |
| Postgres 為 source of truth + Redis ephemeral 熱路徑 | STANDARD | [Alibaba 搶票架構](https://www.alibabacloud.com/blog/system-stability-assurance-for-large-scale-flash-sales_596968) 直接說明:庫存在 Redis,持久化資料庫為 authoritative。[Stripe 庫存 pattern](https://stripe.dev/blog/how-do-i-store-inventory-data-in-my-stripe-application) 一樣 |
| `appendonly no` + `save ""`(純 ephemeral Redis) | STANDARD | [AWS ElastiCache caching patterns](https://docs.aws.amazon.com/whitepapers/latest/database-caching-strategies-using-redis/caching-patterns.html) 直接支援。Memcached 的設計原則一模一樣 |
| 啟動 rehydrate 用 SETNX(保留 in-flight 值) | NOVEL but sound | 沒有公開 reference 專門寫 SETNX-not-SET,但底層邏輯 — 不要覆蓋進行中的值 — 是對的 |
| Lua atomic deduct(DECRBY + XADD) | STANDARD | Alibaba 直接點名:「LUA scripts 的 transaction 特性」用於「先讀剩餘庫存後扣減」。[Games24x7 case study](https://medium.com/@Games24x7Tech/running-atomicity-consistency-use-case-at-scale-using-lua-scripts-in-redis-372ebc23b58e) 證明可以 scale |
| 週期性漂移對帳 | STANDARD(公開資料少) | [SPIFFE issue #4973](https://github.com/spiffe/spire/issues/4973) 明確建議「週期性的 full-db reconciliation of cache」。普遍實踐;但很少人寫出來 |
| 沒有分散式鎖(一致性靠 Lua + outbox) | STANDARD | [Redis 官方 guidance](https://redis.io/glossary/redis-lock/) 明確不建議拿 SETNX-based locks 當主要機制,要用 fault-tolerant pattern;對我們來說,Lua atomicity + transactional outbox 已經涵蓋一致性需求,不需要 locking |

**唯一一個 NOVEL 的項目是 SETNX-on-rehydrate 這個 refinement**。它是標準 pattern(從 source of truth rehydrate cache)的合理 refinement、不是 anti-pattern,理由如下:

1. **要解決的問題**:rehydrate 在 app 啟動時跑。如果 Redis 已經有值(另一個 instance 還在跑、或 app 重啟時其他 peer 維持 Redis 存活、或部署滾動更新時新舊 pod 重疊),naive `SET` 會覆蓋 live 的 in-flight 值 — **那些已經被 Lua 扣減、但 worker 還沒 commit 到 DB 的數量**。
2. **不用 SETNX 會發生什麼**:naive SET 把 counter 重設成 DB 的 `available_tickets`。已經被 Lua 扣減但 worker 還沒 commit 的 in-flight 訂單會被「反扣減」回去 — 結果是**雙重配票**(同一張票賣兩次:一次給 in-flight 訂單佔走、另一次被覆蓋後重新分配出去)。這是正確性 bug,不是效能問題。
3. **為什麼 SETNX 是對的 primitive**:語義剛好對到「只在沒有現有值時才寫」;單一 round-trip,沒有 GET-then-SET 的 race window;Redis 原生指令,行為文件化得很清楚,不需要自訂 Lua。
4. **為什麼是 refinement、不是 novel pattern**:底層 pattern(從 source of truth rehydrate cache)是業界標準。SETNX 而不是 SET 的選擇是這個標準 pattern 的一個邊界 case 處理 — 任何認真實作 rehydrate 的工程師,從根本原則推導都會走到同一個地方。SETNX 本身是 Redis 從 1990 年代就有的指令(現在被 `SET ... NX` 收編);用法毫不創新。
5. **caveat**:SETNX 會保留**任何**現有值,包括錯的。如果 Redis 有壞掉的舊值(來自另一個 bug、或 operator 手動 `SET event:{uuid}:qty 999`),SETNX 會保留錯誤。這個盲點靠下一輪 sweep 的 drift detector 補上 — `cache_high` 方向的偵測就是針對這類「Redis > DB」情況。換句話說:**rehydrate 不負責檢測腐敗、drift detector 才負責**。兩個機制職責分開,任一邊的失敗模式不會污染另一邊。

這次研究最讓我意外的發現:**週期性漂移對帳這個 pattern 是「標準但很少公開記錄」**。每個 senior 工程師私下問都會說「當然你會對帳 cache vs truth」這是顯而易見的;但幾乎沒有 engineering blog 把它寫出來。SPIFFE 的 GitHub issue 是最接近公開 reference 的。這代表:(a)我們做這件事是處在好的群體中,但也代表(b)**我們在記錄 PR-D 漂移偵測器跟 runbook 的同時,寫出了這個 pattern 比較公開的 reference 之一**。

## Lessons

### 411 筆訊息的遺失不是 bug — 是架構本身不一致

實作只是忠實地執行了「Redis 在這裡做什麼」這個問題在每個 commit 當下最順手的詮釋。沒有出錯的 assertion、沒有傳統意義上漏掉的 error handler;**每一行程式碼都在做它的作者意圖的事**。bug 住在實作之間的縫隙裡:deduct 路徑的作者、worker 的 NOGROUP 自癒、用 FLUSHALL 的 reset script、saga compensator 的作者,**每個人對「哪個 store 是 authoritative」的心智模型都不太一樣 — 而這些心智模型沒有任何一個被寫下來**。

修復不是加 error handling。**修復是把契約寫下來,然後改實作對齊**。契約現在在 `docs/architectural_backlog.md`,diff history 有完整紀錄。未來的我在 review 未來的 PR 時,有定義能拿來檢驗。

### 「把快取當 durable storage」這個 anti-pattern 很狡猾

**事件發生前,如果你問我我們的系統有沒有把 Redis 當 durable,我會回答沒有**。我們有 Postgres、有 outbox、有 saga。我們顯然知道 Redis 是 ephemeral。然後我去程式碼裡找這個假設,卻發現 worker 的 NOGROUP 自癒寫著「從 `$` 重建 group」 — **這個動作只在「之後進來的訊息已經夠用、之前的可以丟」這個前提下才合理。而這個前提只在 Redis 是 durable 時才成立。但它不是**。我們已經口頭說過不是問題的那個地方,程式碼的不一致剛好踩在那。

回頭看會盯的訊號:**任何從快取遺失中復原、但沒有做相當於「從 source of truth rehydrate」動作的程式碼,要嘛你在主張一個你沒有的 durability,要嘛你在丟資料**。我們的 NOGROUP 自癒是後者;直到丟了 411 筆訊息我們才注意到。

### 漂移偵測是 observability、不是 auto-correction

PR-D 的 `InventoryDriftDetector` **不會自動修正**。它偵測漂移、命名失敗模式、通知 on-call。三個不同的 reviewer 都問過某個版本的「這不是應該也修掉它找到的漂移嗎?」答案是不:

- **Auto-correction 跟 in-flight Lua deducts 競爭**。當客戶用 Lua 在 decrement 同一個 key 時,從 sweep loop 寫到 Redis key,正是原本架構設計要避開的多寫者 race。
- **operator 看不到的 auto-correction 會把底層失敗藏起來**。如果 saga 補償在 production 出現 desync,「漂移偵測器悄悄修掉它」會讓 bug 活幾個月。「漂移偵測器以 `direction=cache_high` 通知 on-call」會在幾分鐘內把 bug 浮上來。
- **復原動作 — 從 DB truth 重跑 rehydrate — 破壞性夠強(會碰 live 快取),應該由 operator 控制,不該自己跑**。

**保守預設 — 偵測 + 命名 + 告警、不要修 — 是 production observability 應該長的樣子**。「Self-healing」是個行銷用詞;在分散式系統裡,大多數 self-healing 都是**默默把症狀壓下去**。

### 我會做不一樣的事

三件事,以我有多少信心排序:

**1. 在第一個 PR 之前就把契約文件寫好**。我是在 PR-A 期間才寫契約,當時已經開始寫 code 了。**它是專案中單一個槓桿率最高的寫作**;早一點做會讓 PR 序列更乾淨、review 討論更短。這是我會教自己未來十年的 Gall's law 變體:**一個架構決策,如果無法用兩段話寫下來,大概還沒被做出來**。

**2. 把雙語文件契約當成架構的可執行 spec**。「變更實際落地了嗎?」這個問題,最有價值的測試居然是雙語的 `docs/monitoring.md` + `docs/monitoring.zh-TW.md` 更新。如果一個 metric 或 alert 沒在兩邊都記錄,PostToolUse hook 會抓到;如果 prose 模糊,**翻譯 EN 版本到 zh-TW 的動作會逼它變精確**。**雙語契約不是文件衛生 — 它是讓人不得不把話想清楚的 forcing function**。

**3. 在 PR-D 的 review cycle 早一點跑 multi-agent review**。reviewer 找到的兩個 HIGH(不安全的 `errors.Is(err, ctx.Err())` pattern + `GetInventory` 的 `(0, nil)` 歧義)都微妙到我會 ship 它們。修復很小(~30 LOC、跨兩個 commit)。**代價是 PR 落地延遲一天;好處是避免兩個潛在 bug 形狀的 finding 在一個月後打到一個無關的 PR**。我現在把「multi-agent review 有沒有說 HIGH?」當成 merge gate,不是「nice-to-have」 — 對任何碰到 resilience surface 的 PR 都是。

## 接下來

這是計畫中五篇系列的第一篇([`docs/blog/README.md`](README.md))。第二篇會涵蓋 Lua single-thread 在 8,330 acc/s 的天花板,以及怎麼想下一個 10× — 對應 [VU 壓測 benchmark archive](../benchmarks/) 跟我們在 Phase 2 做的 1M QPS 分析。

**Cache-truth 契約在 Phase 3 設計工作中持續產生紅利**。當 Pattern A 預訂流程需要把訂單狀態機擴展到 `awaiting_payment`,「預訂 TTL 的 source of truth 是什麼?」這個問題只需要一句話:Postgres `orders.reserved_until` 是 source of truth,Redis 端的反映(如果有)依 cache-truth 契約處理 — 它們之間的漂移是可偵測且有名字的,不是被魔法修掉。**v0.4.0 的工作把一類沒解的問題變成一條我們持續套用的規則**。

---

*如果文章有錯或漏掉什麼,請對 [`docs/blog/2026-05-cache-truth-architecture.zh-TW.md`](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-cache-truth-architecture.zh-TW.md) 開 issue 或 PR。**Blog 放在 in-repo 就是為了讓修正走跟程式碼一樣的 review 流程。***
