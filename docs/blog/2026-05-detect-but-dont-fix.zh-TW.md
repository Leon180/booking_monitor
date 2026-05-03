# 偵測但不修 — saga / watchdog / drift detector 的三層分工

> English version: [2026-05-detect-but-dont-fix.md](2026-05-detect-but-dont-fix.md)

> **TL;DR.** 我們有 4 層保護:saga compensator(event-driven)+ saga watchdog / recon / drift detector(各自週期 sweep)。**全部都不自動修狀態 — 只偵測、命名、通知 on-call**。三個 reviewer 都問過「這不是應該也修嗎」,答案是不:auto-correction 跟 in-flight 操作競爭、藏住底層失敗、剝奪 operator 對系統的學習。這篇寫每一層對到的失敗模式、為什麼分這四個而不是兩個或八個、以及「責任分開、彼此不污染」這條設計規則。

對應 [PR #45 (recon)](https://github.com/Leon180/booking_monitor/pull/45)、[PR #49 (saga watchdog)](https://github.com/Leon180/booking_monitor/pull/49)、[PR #76 (drift detector)](https://github.com/Leon180/booking_monitor/pull/76)。系列模板見 [`docs/blog/README.md`](README.md)。

## Context

我們的 booking pipeline 在出錯時會走以下幾條路徑:

```
正常:   API → Lua → stream → worker → DB → outbox → Kafka → payment
失敗 1: payment 回 fail → saga compensator(event-driven)
失敗 2: saga 沒收到 / 訊息掉了 → saga watchdog(週期掃描)
失敗 3: worker crash 在 Charging → recon(週期掃描 gateway)
失敗 4: Redis 跟 DB 漂移 → drift detector(週期比對)
```

每一條對到一個真實發生過的失敗:
- saga compensator 是 day 1 設計
- saga watchdog 來自 PR #49(Phase 2 A5):有 24h 卡在 Failed 沒被補償的觀察
- recon 來自 PR #45(Phase 2 A4):有 worker crash 後 Charging 訂單 stuck > 1h 的現象
- drift detector 來自 PR #76(cache-truth roadmap):FLUSHALL 後 411 訊息消失

四個一起組成「失敗但仍可恢復」的安全網。但設計上有一條**共同的明確規則:全部都只偵測,不自動修**。

## Options Considered

### Option 1: 單層 saga compensator(只信 event-driven)
**DISPROVED**。Kafka 訊息會掉(retention 過期、broker crash 沒同步、消費者位移錯)、saga compensator 自己 panic、`order.failed` 訊息沒發出去。**單層保證不夠。**

### Option 2: Auto-correcting 多層(detect AND fix)
**REJECTED**。auto-correction 三個問題:
- 跟 in-flight Lua deduct 競爭 race condition
- 把底層 bug 藏住,operator 永遠不知道 saga 補償在 production 出問題
- 復原動作通常是破壞性的(碰 live 快取、改訂單狀態),應該 operator-gated

### Option 3: 多層 + 偵測但不修(我們選的)
- 4 個獨立層,各管不同失敗模式
- 任何一層偵測到異常都通知 on-call,不自動修
- operator 看 metric/alert/runbook 後手動觸發修復

### Option 4: 沒有安全網
**Philosophical joke**。「執行就要做對」,出錯丟 5xx 給客戶。對搶票場景不可接受(訊息丟了 = 客戶 202 但沒訂單),列出來是為了讓「為什麼要 4 層」這個問題顯眼。

## Decision

選 Option 3,把責任拆乾淨。每一層各對應一個明確的失敗模式,它的 detection signal、recovery action 都是 operator-gated。

### 4 層的責任表

| 層 | 觸發來源 | 對應的失敗模式 | Metric / Alert | 復原(operator-gated) |
| :-- | :-- | :-- | :-- | :-- |
| saga compensator | event-driven(`order.failed`) | payment 失敗 → revert Redis + 標 compensated | n/a(成功時靜默) | 無;這層自己處理掉 |
| saga watchdog | 60s sweep | 卡在 Failed 沒被補償(saga 沒收到事件 / 自己壞 / > 24h) | `saga_stuck_failed_orders > 0 for 10m` | 重試 compensator;> 24h 標 `max_age_exceeded` 通知 on-call |
| recon | 120s sweep | 卡在 Charging 沒被解決(worker crash 後沒有 charging→confirmed/failed 轉換) | `recon_stuck_charging_orders > 0 for 5m` | 查 gateway 真實狀態:Charged → confirm、Declined/NotFound → fail、> 24h 強制 fail 通知 on-call |
| drift detector | 60s sweep | Redis 跟 DB 庫存不一致(FLUSHALL 後 / saga 漂移 / 手動 SetInventory) | `inventory_drift_detected_total{direction}` | direction-specific runbook 分支;通常觸發 rehydrate 或 saga 補償 |

### 為什麼是 4 層而不是 2 層或 8 層

**4 層的論點**:每一層偵測的失敗形狀都不一樣,合併會藏住其中之一。
- saga compensator 是 happy-path-ish-async。work 時看不到。
- saga watchdog 是「event 沒到」的安全網。只有 saga compensator 的話,Kafka 訊息掉就完蛋。
- recon 是「狀態轉換中途死了」的安全網。只有 saga 的話,worker 在 Charging 階段 crash 不會發 `order.failed`。
- drift detector 是「DB 跟 cache 不一致」的安全網。它跟 saga 完全無關 — 偵測的是 Redis-side 的問題。

**為什麼不 8 層**:再加幾層就 diminishing returns。每多一層 = 更多 maintenance + alert noise。4 層是觀察過 90% 失敗模式之後的最小覆蓋集。

### 但 saga compensator 是個例外

注意 saga compensator 是 **event-driven** 而不是 sweep-driven。它收到 `order.failed` 就跑。**它是「自動修」嗎?**

技術上是,語義上不是 — 因為它在「該事件正常路徑的一部分」上跑,不是「獨立的監控層」。對失敗的 booking 而言,saga 補償就是 happy path 結尾的一部分。所以它不破壞 detect-but-don't-fix 規則 — 它根本不是 detect 層,是 happy path 處理器。

## Result

### 量化結果

PR-D 落地後跑 cache-truth smoke test(原本造成 411 訊息消失的那個):

| 訊號 | 之前 | 之後 |
| :-- | :-- | :-- |
| 411 訊息遺失 | 沒人發現 | `ConsumerGroupRecreated` 60s 內告警 + drift detector 確認 cache_missing |
| Saga compensator 自己壞 | 訊息卡住直到有人查 | `saga_watchdog_resolved_total{outcome=compensator_error}` 立即可見 |
| Worker crash on Charging | 訂單 stuck > 1 hour | `recon_stuck_charging_orders` 5m 內告警 |
| Redis 跟 DB 漂移 | 客戶看到 sold_out 但 DB 有票 | `inventory_drift_detected_total{direction}` 60s sweep 偵測 |

每一層覆蓋的失敗模式都被驗證過。**4 層全部偵測的同時、沒有任何一層自動修**。

### 業界對齊

「detect-but-don't-fix multi-layer safety」是業界共識:

- **Stripe Idempotency Keys post**:idempotency store 跟 charge-engine 職責分開。idempotency store 出問題時 charge-engine 不自己重試 — page on-call。
- **AWS reconciliation patterns**:reconciliation jobs 偵測差異、產生 reports,**operator 手動 trigger 修復**。
- **Google SRE Book Chapter 22 (Cascading Failures)**:auto-recovery 在缺乏明確 invariants 的情況下會放大問題。
- **Shopify on auto-healing**:auto-healing is anti-pattern when 它意味著 compromising observability。

4 層 detect-but-don't-fix 的設計跟這條線完全對得上。

## Lessons

### 1. Auto-healing 是個行銷詞、不是工程概念

之前看到「self-healing system」會聯想到 K8s + service mesh 的健康良好。實際工作後發現:**95% 的 self-healing 在分散式系統裡是「悄悄壓掉症狀讓 bug 再活幾天」**。真正的 self-healing(K8s pod restart、TCP retry)發生在 well-defined invariant 之內;跨業務邏輯的 auto-correction 通常不在那個範圍裡。

### 2. 「為什麼這層不修?」是好問題、好答案

三個 reviewer 都問過這個。答案不是「太難了」,是「**修了會藏住 underlying bug**」。寫得清楚的話,這個答案能說服 reviewer;寫不清楚會被說「systemic 偷懶」。下次有人問同樣的問題,我會直接指這個 lesson。

### 3. 多層之間「責任不重複」是基本要求

如果 saga watchdog 跟 recon 都監控同一個訂單的同一個失敗模式,operator 會看到兩個 alert 同時 fire 然後不知道該信哪一個。設計上每層只看一個失敗模式 — `outcome` label / `direction` label 都明確區分。**4 層之間 0 重疊**。

未來加第 5 層(例如 outbox relay watchdog),它必須對到一個還沒被覆蓋的失敗模式;否則就是把現有 4 層的責任拆得太細,引入 alert noise。

## 接下來

第 4 / 5 篇看時機:O3.2 變體 B 跑完 bare-metal benchmark 後寫 Docker Mac NAT cap 那篇,或是 Pattern A schema 設計成熟後寫 section-level sharding 設計篇。

對 Phase 3 來說,這 4 層安全網**會在 Pattern A 完成時擴張**:
- 新加 reservation expiry sweeper(處理 `awaiting_payment` 過期沒付款的訂單)→ 第 5 層
- saga compensator 的 scope 縮小到 `{expired, payment_failed}`(payment 路徑改成 webhook-based,正常路徑不再走 saga)

Reservation expiry sweeper 設計上仍然是 detect-but-don't-fix:它偵測到 `reserved_until < now()` 的訂單時,**operator-gated 觸發 revert**(把 Redis 票數加回去 + 標記 expired)。跟 recon 結構一樣,只是觸發條件不同。

---

*如果文章有錯或漏掉什麼,請對 [`docs/blog/2026-05-detect-but-dont-fix.zh-TW.md`](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-detect-but-dont-fix.zh-TW.md) 開 issue 或 PR。*
