# Inventory Hold Pattern — 設計筆記

> **Status**: 設計階段(D4.1 前置研究)
> **Date**: 2026-05-04
> **Decision owner**: Leon
> **Related**: `docs/design/ticket_pricing.md`、 D4.1 / D5 / D6 / D8

## 1. 問題陳述

當 user 在 flash sale 場景搶票時,系統需要追蹤一個關鍵狀態:
**「這個 user 對這批庫存有合法的暫時持有(hold),其他人不能搶,他可以繼續往下一步走(選位 / 付款)。」**

這個狀態怎麼存、什麼是 key、TTL 怎麼處理、怎麼跟防詐解耦 — 是大型 ticketing 系統的核心設計問題。

## 2. 三層 concern 拆解 — 不要混在一起

| 層 | 解什麼問題 | 持有期 | 失敗補償 | Storage |
|---|---|---|---|---|
| **庫存持有 (inventory hold)** | 「這 N 張票是這個人的,別人別搶」 | 短(reservation TTL,15 min) | TTL 過期自動釋放 + saga 補回 | DB(必須持久) |
| **Cart / Session** | 「user 正在 checkout,剛剛點了 quota X」 | 中(瀏覽器 session) | 關 tab 就掉 | Redis or cookie |
| **Anti-fraud / Rate limit** | 「同一 IP 在 1 秒內打了 100 次 /book」 | 短(sliding window,秒級) | bucket 過了就 reset | nginx zone / Redis sliding |

**最大的設計錯誤是把這三層塞同一張表**。具體例子:
- 把 IP 寫進 inventory hold table → mobile 用戶切換網路 IP 變了,系統以為他不能繼續操作(legitimate user 被擋)
- 把 cart session 跟 inventory hold 混在一張表 → 關 tab 應該掉 cart 但庫存應該維持 hold,語意衝突
- 把 rate limit 寫進業務表 → rate limit 演進(換成更激進的 sliding window)需要改業務 schema

## 3. 業界做法摘要

| 平台 | hold 機制 | source confidence |
|---|---|---|
| **Pretix** (open source) | `CartPosition` 表(`cart_id` + `item_id`),session-bound,有 TTL | ✅ 直接看 [GitHub source](https://github.com/pretix/pretix/blob/master/src/pretix/base/models/orders.py) |
| **Eventbrite** | order 在 `pending` 狀態即是 hold | 🟡 從 user-facing 行為推論(沒看過內部 schema) |
| **Stripe Checkout** | `Checkout Session` 在 `open` 狀態即是 hold(session_id 是 key) | ✅ Stripe API doc |
| **Shopify** | `Cart` 表 + `cart_token`(session-based) | ✅ Shopify Storefront API doc |
| **航空 GDS** | `PNR`(Passenger Name Record)在 ticketed 之前是 hold | ✅ IATA EDIFACT/NDC 文件 |
| **KKTIX** | 推論是「order pre-payment 狀態」,15 分鐘倒數即是 reservation TTL | 🟡 user-facing 行為推論,**沒看過實際 schema** |
| **Ticketmaster** | "Verified Fan" 階段用 queue token,搶到後變成 hold | 🟡 對外行銷文件提及,內部 schema 未公開 |

**圖騰結論**:
**「hold = 預下單狀態的 order/session」**這個 pattern 是 Stripe / 航空 / 多數電商的共識。Pretix 是少數做 cart 跟 order 兩表分離的(Django ORM 偏好的拆法)。

## 4. 決定

### **採用「order = hold」path**(對應 Stripe Checkout Session / Eventbrite / KKTIX 推論模型)

理由:
1. **Pattern A 已經把 order lifecycle 設計成這樣**: `awaiting_payment → paid | expired | payment_failed`。 `awaiting_payment` 狀態的 row 本身就是 hold。
2. **D6 expiry sweeper 已規劃 hold 過期清理機制** — `WHERE status='awaiting_payment' AND reserved_until < NOW()` 直接 cover 「過期釋放庫存」
3. **避免兩個 source of truth** — 「Redis 庫存 / cart 表 / order 表」三邊要 sync,工程複雜度暴增,但收穫只有一個 nice-to-have(匿名搶票 / 多票種一次結帳)
4. **portfolio scope 範圍內最簡潔正確的選擇** — 不需要證明能畫 Pretix 級複雜 schema,需要證明能合理判斷簡化邊界

### Quota 概念在我們系統的具體 mapping

```
「quota」抽象概念       →  我們的 ticket_type 表 (D4.1 之後)
「user 持有 quota」      →  orders.ticket_type_id + orders.user_id + orders.status='awaiting_payment'
「拿到 quota 才能選位」  →  Pattern A 的 reserved_until 期間 user 可做的事
                            (D8 加 seat 後,這期間就是 seat selection window)
```

→ Quota 設計 **完全對齊現有架構**,不需要新 entity。

### 流程圖

```
user 搶票:
  → BookingService.BookTicket(ticket_type_id, quantity)
  → Lua deduct (Redis)            -- ticket_type 庫存原子預扣
  → 寫 stream → worker             -- 非同步處理
  → INSERT INTO orders             -- 持久化 hold
       (user_id, ticket_type_id, status='awaiting_payment',
        reserved_until = NOW() + 15min, amount_cents = SNAPSHOT)

user 之後操作 (選位 / 付款 / 取消):
  → SELECT * FROM orders
       WHERE user_id = $me
         AND ticket_type_id = $X
         AND status = 'awaiting_payment'
  → ↑ 這就是「user 持有 quota」的證據,一句 SQL,有 index

D6 過期清理:
  → 每 N 秒掃 WHERE status='awaiting_payment' AND reserved_until < NOW()
  → MarkExpired → saga compensator 回補 Redis 庫存
```

## 5. Anti-fraud 層 — 屬於 middleware,不在這裡

IP-based 防詐 / rate limit 是**獨立 middleware 層**,跟 inventory hold 完全解耦:

```
Stack:
  ┌─────────────────────────────────────────┐
  │ nginx limit_req_zone (per-IP,秒級粗篩) │   ← 已有
  ├─────────────────────────────────────────┤
  │ application middleware:                 │
  │   - Per-IP sliding window (Redis)       │   ← future N9.x
  │   - Per-user rate limit                 │   ← future N9.x
  │   - Bot detection (UA / fingerprint)    │   ← 進階,可不做
  ├─────────────────────────────────────────┤
  │ business handlers (booking, /pay)       │   ← 不該知道 IP
  └─────────────────────────────────────────┘
```

**Redis sliding window key shape**:
```
ratelimit:ip:{ip}:1m              → counter, TTL 60s
ratelimit:user:{user_id}:event:{event_id}:1h → counter, TTL 1h(防囤票)
```

→ 這些跟 orders 表完全解耦。 業務代碼不該看到 `request.ip`。

**為什麼 IP 不該寫進 hold 表的具體理由**:
- Mobile 用戶切換網路 → IP 變 → 誤判為「不能繼續操作」(legitimate user 被擋)
- 公司 / 校園 NAT 後面好幾百個 user 共用 IP → 一個人搶到鎖死整個 IP
- VPN / 代理 → 不對齊真實使用者
- IP 是請求屬性,不是 user 屬性 — 寫進 user-bound 業務表是 layering violation

## 6. 不在當前 scope 的 future expansion

### Multi-ticket-type cart(Pretix CartPosition path)

**觸發條件**: 業務需要「同一個 checkout 買多張不同 ticket_type」(例:VIP × 1 + GA × 2 一次付款,共一個 PaymentIntent)。

**實作**:
- 新增 `cart_positions` 表 (`cart_id` + `ticket_type_id` + `quantity` + `reserved_until`)
- `cart_id` 是 session-bound,可 anonymous(不需 user_id)
- Cart → Order 是 checkout 一刻的 commit
- order 上掛 multiple `order_items[]`(ticket_type × quantity × amount_cents 的 tuple)

### Anonymous booking(無需登入即可搶票)

**觸發條件**: 「想讓觀光客 / 不註冊 user 直接搶票」的 UX 需求。

**實作**: 把 `user_id` 改成 nullable + 加 `cart_token` (session id)。 cart_token 是匿名 hold 的 key。

→ 現在不做。 假設 user 都登入。

### Per-user rate limiting / 防囤票

**觸發條件**: 真實上線後發現 single user 搶 100 張轉售。

**實作**: middleware 層加 `ratelimit:user:{id}:event:{event_id}:1h` counter,超過閾值 reject。

→ 排在 N9 (Auth/RBAC) 之後,做為 N9.x 的 follow-up。

### 多 ticket_type 共享同一個庫存桶(Pretix Quota model)

**觸發條件**: 業務需要「早鳥 + 一般共用 100 張庫存,先賣完哪個就停哪個」。

**實作**: 引入 `inventory_quotas` 表,ticket_type 跟 quota 是 M:N。

→ 現在不做。 大多數真實 ticketing 場景一個 ticket_type 一個庫存桶就夠了。

## 7. 引用來源

- [Pretix 原始碼 — orders.py](https://github.com/pretix/pretix/blob/master/src/pretix/base/models/orders.py)
- [Pretix 原始碼 — items.py](https://github.com/pretix/pretix/blob/master/src/pretix/base/models/items.py)
- [Stripe Checkout Session API](https://stripe.com/docs/api/checkout/sessions)
- [Shopify Storefront API — Cart](https://shopify.dev/docs/api/storefront/latest/objects/Cart)
- [OWASP Rate Limiting Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Denial_of_Service_Cheat_Sheet.html)

