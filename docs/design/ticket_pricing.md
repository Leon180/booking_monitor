# Ticket Pricing Schema — 設計筆記

> **Status**: 設計階段(D4.1 前置研究)
> **Date**: 2026-05-04
> **Decision owner**: Leon
> **Related**: `docs/design/inventory_hold.md`、 D4.1 / D8

## 1. 問題陳述

D4 的初稿把 `priceCents` / `currency` 直接寫進 `payment.Service` struct + `BookingConfig`,這個設計暴露了一個架構錯誤:**價格屬於 event(或 ticket type),不屬於 service config**。

這份文件記錄三件事:
1. 業界真實的訂價 schema 長什麼樣 
2. 我們 D4.1 該怎麼設計才對齊主流
3. 為什麼某些「看起來合理」的選擇我們不採用

## 2. 真實 ticketing 系統的訂價維度

業界(Stripe / KKTIX / Ticketmaster / Eventbrite / 航空)實際在處理的訂價層次,從簡到繁:

| # | 維度 | 範例 | 我們會碰到嗎? |
|---|---|---|---|
| 1 | **Per-event flat price** | 電影票 $300 一張 | ✅ Phase 3 demo 必要 |
| 2 | **Per-section tier** | 演唱會 VIP $5000 / A 區 $3000 / B $2000 / C $1000 | ✅ D4.1 / D8 |
| 3 | **Per-seat** | 對號入座場館,同一區內前排比後排貴 | ❌ 不在 portfolio scope |
| 4 | **Dynamic pricing** | early bird 折扣 / surge / 最後一小時清倉 | 🟡 schema 預留(`sale_starts_at`) |
| 5 | **Promo codes** | 折扣碼套用在 checkout | ❌ 不做 |
| 6 | **Quantity tier discount** | 買 4 張以上九折 | ❌ 不做 |
| 7 | **Tax** | 台灣 5% / 美國各州不同 | ❌ Stripe Tax 接管 |
| 8 | **Service fees** | Ticketmaster 風格便利費 | ❌ 不做 |
| 9 | **Multi-currency** | 同一活動依區域定價 USD/TWD/EUR | 🟡 schema 預留(`currency` 欄位) |
| 10 | **Price snapshot at order time** | 訂單記錄成交那刻的價格,不靠下單後重新查詢 | ✅ **必做 — 業界共識** |

### 第 10 點 —「下單時 snapshot 價格」是不可省略的

所有正式的 e-commerce 系統都把訂單下單時的價格寫死在 order 上,不在 /pay 那刻才去查。 理由:
- **Race avoidance**: event 方在 user book 跟 user pay 之間改了價格,user 看到的應該是 book 當下的價,不是 pay 當下的
- **Audit / dispute**: 客戶投訴「我明明 5/3 看到 $300 為什麼刷了 $350」,operator 要能直接從 order row 讀 snapshot
- **Legal / accounting**: 財報以實收金額為準,不是當前定價
- **Stripe alignment**: PaymentIntent 的 `amount` 在 intent 建立時就 immutable,跟 order snapshot 是同一個語意

→ Order **必須**有 `amount_cents` + `currency`,/pay **不能**從 ticket_type 重查。

## 3. 業界 schema 對比

### Pretix(open source,可直接看 code)

```python
# https://github.com/pretix/pretix/blob/master/src/pretix/base/models/items.py
class Item(models.Model):           # ← 票種
    event       = ForeignKey(Event)
    name        = CharField
    default_price = DecimalField     # 訂價
    available_from = DateTimeField   # 開賣時間
    available_until = DateTimeField  # 截止
    # 注意: Item 本身沒有 quantity!

class Quota(models.Model):          # ← 庫存桶(獨立於 Item)
    event       = ForeignKey(Event)
    size        = IntegerField       # 總張數
    # available_count 從 Order 即時計算 (有 cache layer)

class Seat(models.Model):           # ← 座位 layer
    event           = ForeignKey(Event)
    seating_plan    = ForeignKey(SeatingPlan)
    product         = ForeignKey(Item, null=True)
    blocked         = BooleanField
```

**洞察**: Pretix 拆三層 — `Item`(訂價/銷售規則)/ `Quota`(庫存桶)/ `Seat`(物理位置)。 三者解耦,M:N 關係。

### 航空 fare class 模型(IATA 文件)✅

```
Flight (航班)
  └── Fare Class (艙等 Y/M/B/H/...) — 訂價 + capacity (denormalized)
  └── Seat (具體座位)

訂票流程:
  1. fare class capacity 先預扣 (PNR 在 GDS)
  2. seat assignment 是另一個 transaction(可選,可後做)
  3. 同一個 fare class 內的 seat 可換 — 庫存以 fare class 為單位記
```

→ **航空業 fare class 上是 denormalize available_seats 的**,具體 seat 是另一張表 join 過去。 跟 Leon 設計直覺一致。

### Stripe Product / Price 模型 ✅

```
Product       — 商品本身(name, description)
  └── Price[] — 訂價(amount, currency, billing_interval)
                 一個 product 可以有多個 price (不同貨幣 / 不同訂閱週期)

Subscription / PaymentIntent reference 到 Price.id, 不是 Product.id
```

→ **「訂價」是獨立 entity,跟「商品」(event) 解耦,跟「庫存」也解耦**。 我們的 `event` 應該是 product,`ticket_type` 應該是 price + inventory 的合體。

### KKTIX / Ticketmaster / Eventbrite 內部 schema 🟡

❓ **老實講: 我沒看過任何一家的實際 schema**。 只能從 user-facing API、招聘 JD、blog 內容推論。

從 KKTIX 的對外 API 行為 (我用過、讀過 doc):
- 一個 event 可以有多個 ticket type
- 每個 ticket type 有獨立的 quantity / sale window / 限購數量
- 對號入座的 event 才有 seat layer
- ticket type 跟 seat 是分開的銷售單位

→ 推論他們**至少**有 `events / ticket_types / seats` 三層,但**具體欄位、是否 denormalize 庫存、用什麼 atomicity**(Postgres MVCC vs Redis Lua vs Cassandra LWT)— **我不知道**。 對外 portfolio 不能拍胸脯說「我複製 KKTIX」。

## 4. KKTIX 風格的「票種」概念 — 跟「分區」不一樣

KKTIX 的核心概念叫**「票種」(ticket type)**,不是 section 也不是 level。 觀察實際演唱會 event 頁(我作為 user 用過):

```
[周杰倫 2026 演唱會]

票種:
  ✅ VIP A區早鳥票      NT$5,500   (剩 12)   開賣 5/3 12:00 - 5/10 23:59
  ✅ VIP A區一般票      NT$5,800   (剩 8)
  ✅ A區早鳥票          NT$3,500   (剩 45)
  ✅ A區一般票          NT$3,800
  ✅ 學生票C區          NT$1,200   (限學生)
  ✅ 套票(VIP+T恤)      NT$8,000   (剩 100)
```

每一個票種是**訂價 + 庫存的最小單位**。 屬性:

```
ticket_type: {
  id, event_id, name, price_cents, currency,
  total / available,
  sale_starts_at / sale_ends_at,    // early bird 機制
  per_user_limit,                   // 每人限購
  area_label? (optional metadata),  // 物理區域顯示名,沒有就 NULL
  requires?: ["student_id"]         // 學生票
}
```

**Section 跟 Ticket Type 是兩個獨立概念**,在 KKTIX 等大型平台上常常以笛卡爾積形式呈現:

| 維度 | 是什麼 | 範例 |
|---|---|---|
| **Section / 分區** | 物理屬性(座位在哪) | VIP / A / B / C 區 |
| **Ticket Type / 票種** | 訂價單位 + 庫存單位 + 銷售規則 | 早鳥 / 一般 / 學生 / 套票 |

實務上有 4 種 schema pattern:

| 模式 | 範例 | 推薦使用 |
|---|---|---|
| **1 section × 1 type** | 電影票 | flat events / 工作坊 |
| **N sections × 1 type per section** | 小型演唱會:VIP/A/B 各一個價 | 中型場館 |
| **1 type with no section** | 講座 | 線上活動 |
| **N sections × M tiers** | 大型演唱會 KKTIX 級 | 大型場館 |

## 5. 我們 D4.1 的具體 schema 提案

### 採用「方案 A 升級版」: rename `event_sections` → `event_ticket_types`,加 price + sale_window

```sql
-- 000014: Pricing + ticket_types(D4.1)
ALTER TABLE event_sections RENAME TO event_ticket_types;
ALTER TABLE event_ticket_types
    ADD COLUMN price_cents BIGINT NOT NULL,
    ADD COLUMN currency VARCHAR(3) NOT NULL,
    ADD COLUMN sale_starts_at TIMESTAMPTZ NULL,  -- early bird 機制(預留)
    ADD COLUMN sale_ends_at TIMESTAMPTZ NULL,
    ADD COLUMN per_user_limit INT NULL,          -- 每人限購(預留)
    ADD COLUMN area_label VARCHAR(255) NULL;     -- 物理區域顯示名(D8 才會用)

-- name 語意延伸: 從「區域名」變成「票種名」("VIP A 區早鳥票")
-- area_label 將來 D8 加 seat layer 時用來標物理區域

-- order 改 reference 到 ticket_type + snapshot 價格
ALTER TABLE orders RENAME COLUMN section_id TO ticket_type_id;
ALTER TABLE orders
    ADD COLUMN amount_cents BIGINT NULL,         -- 暫 nullable, 新訂單必有
    ADD COLUMN currency VARCHAR(3) NULL;
```

**為何傾向方案 A 升級版**(non-empirical 部分):
1. **80% KKTIX 模型,20% 工作量**。大型演唱會主要瓶頸是「VIP / A / B / C 不同價」,不是「同區早鳥/一般」。 後者用一個 ticket_type per (section, tier) 就能 cover,不需要兩維 schema
2. **與 D1 設計意圖一致**。 D1 加 `event_sections` 的真正用意是「inventory + sharding 軸」,訂價最終會掛在這個單位上 — 升級成 ticket_type 是 D1 的自然演進
3. **forward-compatible to D8**。 D8 加 `seats` 表,`seat.ticket_type_id` 直接指向 event_ticket_types,不用再改 schema

**join 開銷 — empirical 決策**: 之前我寫「避開 join 開銷」當作排除方案 B 的理由,這是 speculation 不是 measurement。 真的要選 A vs B 時,**先 benchmark**:
- baseline:hot-path(/book)的 p95 + accepted_bookings/s(我們對 D3 / D4 已有)
- 方案 A(單表)vs 方案 B(兩表 join)各跑一次 k6 比對
- 如果 join 帶來的 p95 增幅 < 5%(noise floor 內)→ 方案 B 不該被排除,兩維 schema 對 KKTIX 級複雜場景的表達力是真實 win
- 如果增幅 >10% → 才該選方案 A 的單表合併
- 增幅 5-10% → 看實際 use case 取捨

→ 「不要拿 speculation 當決策依據」這條原則寫在這裡,提醒未來自己。

### 預留 D8 (seat layer) 的 schema

```sql
-- 000020 (預想中, D8 範圍): Seat layer 給對號入座場景
CREATE TABLE seats (
    id UUID PRIMARY KEY,
    ticket_type_id UUID NOT NULL,
    seat_label VARCHAR(50) NOT NULL,      -- "A1-12" 之類
    status VARCHAR(20) NOT NULL,          -- 'available' / 'reserved' / 'sold'
    reserved_until TIMESTAMPTZ NULL,
    order_id UUID NULL,
    UNIQUE (ticket_type_id, seat_label)
);

-- ticket_type 上的 available_tickets 維持 denormalized,跟 seat 透過 saga 對齊
```

→ D4.1 schema 是 forward-compatible 的:現在每個 ticket_type 是「pricing + inventory」,D8 加 seat layer 後變成「pricing + inventory + optional seat assignment」。

## 6. Order snapshot price — 為什麼一定要做

D4.1 必須做 order-side snapshot,不能省略。 原因見 §2 第 10 點。

具體流程:
```
BookingService.BookTicket(user_id, ticket_type_id, quantity):
  1. 讀 ticket_type → 拿 price_cents, currency
  2. 計算 amount_cents = price_cents × quantity
  3. Lua deduct (Redis)             -- ticket_type 庫存原子預扣
  4. 寫 stream → worker
  5. INSERT INTO orders             -- 連同 amount_cents, currency snapshot
       (status='awaiting_payment',
        ticket_type_id, amount_cents, currency, reserved_until = NOW() + 15min)

PaymentService.CreatePaymentIntent(order_id):
  1. 讀 orders → 拿 amount_cents, currency (snapshot)
  2. gateway.CreatePaymentIntent(order_id, amount_cents, currency)
  3. 不需要再讀 ticket_type → 不會被 ticket_type 後續改價影響
```

→ /pay 跟 ticket_type **完全解耦**。 ticket_type 是 presentation,order 是 facts,gateway 拿到的是 immutable amount。

## 7. 業界 dual-implementation pattern(payment gateway 側)

對應到 D4.2 (StripeGateway):

| 模式 | 實作 | 適用場景 |
|---|---|---|
| `PAYMENT_GATEWAY=mock` | 我們的 `MockGateway`(sync.Map) | 單元測試、benchmark、CI |
| `PAYMENT_GATEWAY=stripe_test` | `StripeGateway` 配 `sk_test_...` | 整合測試、demo、k6 端到端 |
| `PAYMENT_GATEWAY=stripe` | 同 `StripeGateway` 配 `sk_live_...` | production(我們不會跑) |

`domain.PaymentGateway` interface 完全不變,fx provider 切換實作。 這是 Shopify / Eventbrite / Toast 都在用的 pattern。

## 8. 不做的事

### Pretix Quota M:N 模型

**觸發條件**: 業務需要「早鳥 + 一般共用 100 張庫存,先賣完哪個就停哪個」。

**Pretix 怎麼做**: 引入 `Quota` entity, ticket_type 跟 quota 是 M:N。

→ 我們不做。 大多數 ticketing 場景一個 ticket_type 一個庫存桶就夠了。 真有共享庫存需求再加。

### Dynamic pricing / surge

**觸發條件**: 真實業務想做 早鳥折扣 / surge / 最後一小時清倉。

**做法**: 在 ticket_type 上加 `pricing_rules` JSONB,或新增 `pricing_modifiers` 表。

→ 我們不做。 portfolio 不需要證明這個。 schema 上 `sale_starts_at` / `sale_ends_at` 已經夠表達「分時段不同 ticket_type」的 early bird,不需要 dynamic pricing。

### Tax / fees / promo codes

**觸發條件**: 商業用途真的要算稅 / 服務費 / 折扣碼。

**做法**: order 上加 `discount_cents` / `tax_cents` / `fee_cents` 欄位 + modifier chain。

→ 我們不做。 等業務需要再加。 Stripe Tax 可以接管稅;promo codes 可以走 Stripe Coupons。

### Multi-currency 動態切換

**觸發條件**: 同一場活動依 user 區域顯示不同貨幣。

**做法**: ticket_type 一個 entity 對多個 (currency, price_cents) tuples,類似 Stripe Price 的 multi-currency 模式。

→ schema 預留 `currency` 欄位,但只用一個值。 真有需求再擴展成 M:N。

## 9. 引用來源(可驗證)

- [Pretix Item / Quota / Seat 原始碼](https://github.com/pretix/pretix/blob/master/src/pretix/base/models/items.py)
- [Stripe Products and Prices doc](https://stripe.com/docs/products-prices/overview)
- [Stripe PaymentIntent API](https://stripe.com/docs/api/payment_intents)
- [IATA NDC Standard](https://www.iata.org/en/programs/airline-distribution/ndc/) (fare class 模型出處)
- [Shopify Variant / Inventory API](https://shopify.dev/docs/api/admin-rest/latest/resources/inventoryitem)
