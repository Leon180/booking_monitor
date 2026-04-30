# Booking Monitor - 專案規格書

> English version: [PROJECT_SPEC.md](PROJECT_SPEC.md)

## 1. 專案概述

一個用來模擬「搶票 / Flash Sale」情境(10 萬以上併發使用者)的高並發票務訂票系統。以 Go 撰寫,遵循 DDD 與 Clean Architecture。

**核心目標**:以多層防線防止超賣,同時將吞吐量最大化。

**開發時程**:2026-02-14 至 2026-02-24(10 天,15 個 commits,12 個 phases)。2026-04-11 進行多 agent 程式碼審查,共整理出 66 項 findings,於 PR #8(CRITICAL)/ #9(HIGH)/ #12(MEDIUM/LOW/NIT)/ #13(可觀測性 + smoke test plan)完成修復。GC 優化接著在 4/12–13 透過 PR #14(baseline harness + quick wins,+157% RPS)與 #15(deep fixes:sync.Pool、escape analysis、GOMEMLIMIT、合併 middleware)完成。Logger 架構在 PR #18(4/23–24)搬到 `internal/log`,加入 ctx-aware emit 方法、OTEL trace_id/span_id 自動注入、以及執行期 `/admin/loglevel` 端點 — 詳見第 8 節。

---

## 2. 系統架構

```
Client --> Nginx (限流: 100 req/s/IP, burst 200)
  --> Gin API (冪等性檢查, Correlation ID, metrics, mapError)
    --> BookingService
      --> Redis Lua Script (原子 DECRBY + XADD 至 stream)
        --> Redis Stream (orders:stream)
          --> WorkerService (consumer group, PEL 恢復)
            --> PostgreSQL 交易 [Order + OutboxEvent]
              --> OutboxRelay (advisory lock 領導者選舉)
                --> Kafka (order.created)
                  --> PaymentWorker (KafkaConsumer)
                    --> 成功: UPDATE status='confirmed'
                    --> 無效輸入: DLQ (order.created.dlq)
                    --> 失敗: Outbox -> Kafka (order.failed)
                      --> SagaCompensator (Redis 持久化重試計數)
                        --> DB: IncrementTicket + status='compensated'
                        --> Redis: INCRBY 後 SET NX EX(crash-safe revert)
                        --> 重試用盡: DLQ (order.failed.dlq)
                                       + saga_poison_messages_total
```

### 資料流摘要

Happy path 與 failure path 都從同一個 `POST /api/v1/book` 呼叫開始,在第 4 步之後分歧。每一個跨元件的邊界都採「at-least-once + idempotent consumer」契約;不會有任何一個同步 RPC 讓 API 回應被 Kafka 或 Postgres 的寫入阻擋住。

#### Happy path

| # | 元件 | 輸入 | 動到的儲存層 | 效果 | 失敗行為 |
|---|------|------|-------------|------|---------|
| 1 | Gin API handler(`/api/v1/book`) | 使用者請求 | Redis(idempotency key,24 小時 TTL) | 先查 `Idempotency-Key` header,命中時把 request fingerprint(body 的 SHA-256)拿去跟快取的 entry 比對:相符 → 原封不動回傳;不符 → 409 Conflict;沒有 fingerprint(legacy entry)→ 回傳 + 懶式寫回 fingerprint。寫入快取時只有 2xx + 5xx 會被快取(4xx 驗證錯誤不快取 — 沿用 Stripe 慣例)。完整契約表見 §5。 | Body 缺漏/格式錯 → 400 `"invalid request body"`;`mapError` 會把任何下游錯誤 sanitize |
| 2 | `BookingService.BookTicket` | `user_id, event_id, quantity` | Redis 透過 `deduct.lua`(原子性) | `DECRBY event:{id}:qty`:`>= 0` 就順便 `XADD orders:stream` 並回 200;`< 0` 就 `INCRBY` 還原,回 409 `sold out` | API 在 Redis 一成功就 return — 訂單**還沒真正寫入 DB**,只是進了 queue |
| 3 | `WorkerService` → `MessageProcessor.Process`(`orders:stream` 上的 consumer group) | Stream 訊息 | PostgreSQL(單一 UoW 交易) | `DecrementTicket`(DB 上的 row-level 二次驗證,抓 Redis/DB drift)→ `orderRepo.Create`(UNIQUE 部分索引擋重複購票)→ `outboxRepo.Create(event_type="order.created")` — **三步在同一個交易裡** | `DecrementTicket` 拒絕 → 還原 Redis + ACK(記錄 inventory conflict metric);`orderRepo.Create` 遇到 `ErrUserAlreadyBought` → 還原 Redis + ACK(記錄 duplicate);其他錯誤 → 不 ACK,`processWithRetry` 跑 3 次,之後進 DLQ(`orders:dlq`)並還原 Redis |
| 4 | `OutboxRelay`(背景 goroutine,透過 Postgres advisory lock 1001 選出唯一 leader) | `events_outbox WHERE processed_at IS NULL` | PostgreSQL(讀 + 更新)→ Kafka topic `order.created` | 每 500ms 輪詢一次(部分索引 `events_outbox_pending_idx` 涵蓋此 query),每個 tick 最多發 100 筆,發完再 `UPDATE processed_at = NOW()`。Publish 失敗 → 跳過 `MarkProcessed`,下一個 tick 重發。Publish 成功但 `MarkProcessed` 失敗 → 下一個 tick 會再發一次,consumer **必須**做到 idempotent | Leader crash → advisory lock 自動釋放(session-bound)→ 某個 standby 下一個 tick 接手 |
| 5 | `KafkaConsumer` → `PaymentService.ProcessOrder` | `OrderCreatedEvent` | Redis(以 `orderRepo.GetByID` → 檢查狀態的方式做 idempotency)→ `PaymentGateway.Charge` → PostgreSQL(更新狀態) | 訂單狀態若已是 `confirmed`/`failed`/`compensated` → 直接跳過(idempotent)。否則呼叫 mock gateway 扣款,成功就 `UPDATE orders SET status='confirmed'`,並 commit Kafka offset | 無法解析 JSON / `ErrInvalidPaymentEvent` → 寫到 `order.created.dlq`(帶 provenance headers)並 commit offset。短暫 DB/Redis 錯誤 → 不 commit,讓 Kafka rebalance 重送 |

到這一步,使用者的訂票已經徹底確認:Redis、DB 和付款狀態三者一致。

#### Failure path(付款閘道拒絕扣款)

| # | 元件 | 輸入 | 動到的儲存層 | 效果 |
|---|------|------|-------------|------|
| 5a | `PaymentService.ProcessOrder`(同上一步的呼叫) | `Charge` 回傳錯誤 | PostgreSQL(單一 UoW 交易) | `UPDATE orders SET status='failed'` + `outboxRepo.Create(event_type="order.failed")` — 同一筆交易,跟第 3 步的 outbox 有相同保證 |
| 5b | `OutboxRelay` | `order.failed` 的 pending row | PostgreSQL → Kafka topic `order.failed` | 跟第 4 步是同一個輪詢迴圈,只是 topic 不同 |
| 5c | `SagaConsumer` → `SagaCompensator.HandleOrderFailed` | `OrderFailedEvent` | PostgreSQL(UoW 交易:`IncrementTicket` + `status='compensated'`)→ Redis 透過 `revert.lua`(`INCRBY event:{id}:qty` → `SET saga:reverted:{order_id} NX EX 7d`) | DB 路徑以 `OrderStatusCompensated` 守門員做 idempotent;Redis 路徑以 `saga:reverted:*` key 做 idempotent |
| 5d | 補償失敗時 | — | Redis(持久化的重試計數 `saga:retry:p{partition}:o{offset}`,TTL 24 小時) | 計數 +1,訊息**不 commit**,交給 Kafka 重送。計數會活過 consumer 重啟 |
| 5e | 超過 `sagaMaxRetries = 3` 次後 | Poison 訊息 | Kafka topic `order.failed.dlq` + 指標 | 原始 payload + provenance headers 寫入 DLQ,`saga_poison_messages_total` 和 `dlq_messages_total{topic, reason="max_retries"}` 遞增,retry 計數清掉,Kafka offset commit。**不會有靜默 drop** |

到這一步,Redis 和 DB 的庫存都回到訂票前的狀態;使用者在歷史紀錄會看到 `status='compensated'`。

#### 跨元件的共通保證

- **API 回應絕不會被 Kafka 或 Postgres 的寫入阻擋。** 只有 Redis 在同步路徑上。
- **每一筆「必須伴隨事件」的 DB 寫入都走 outbox,在同一個交易內 commit。** 整個系統沒有任何一處是 `db.Commit(); publisher.Send()` 這種順序寫。
- **每個 consumer 都是 idempotent** — 方法包含 DB 唯一約束、Redis `SET ... NX`、或檢查「狀態已進入終局」。這是 outbox 的 at-least-once 語義一定要搭配的另一半。
- **沒有靜默的訊息 drop。** 任何無法處理的訊息都會落到一個 DLQ topic,並帶足夠的 provenance headers(原始 topic / partition / offset / reason / error)方便手動 replay。唯一的例外是 `PaymentService` 上的 transient 基礎設施錯誤 — 目前我們依賴 Kafka rebalance 重試,未來的 DLQ worker 會在那裡補上 retry budget。

---

## 3. 領域模型

### Entities

**Event**(`internal/domain/event.go`)
```
ID, Name, TotalTickets, AvailableTickets, Version
不變條件: 0 <= AvailableTickets <= TotalTickets
方法: Deduct(quantity) (*Event, error) — immutable,回傳扣減後的新
      *Event,不會 mutate receiver。
```

**Order**(`internal/domain/order.go`)
```
ID, EventID, UserID, Quantity, Status, CreatedAt
狀態生命週期: pending -> confirmed | pending -> failed -> compensated
唯一性約束: UNIQUE(user_id, event_id) WHERE status != 'failed'
  (部分索引 - 允許付款失敗後重試購買)
```

**OutboxEvent**(`internal/domain/event.go`)
```
ID, EventType, Payload (JSON), Status, ProcessedAt
Types: order.created, order.failed
```

### Domain Interfaces

| Interface | 用途 | 實作 |
|-----------|------|------|
| EventRepository | Event CRUD + GetByIDForUpdate + DecrementTicket / IncrementTicket + Delete(給 CreateEvent 補償使用) | PostgreSQL |
| OrderRepository | Order CRUD + 狀態更新 | PostgreSQL |
| OutboxRepository | Outbox CRUD + ListPending/MarkProcessed | PostgreSQL |
| InventoryRepository | 熱庫存扣減/回滾 | Redis(Lua scripts) |
| OrderQueue | 非同步訂單串流(Enqueue/Dequeue/Ack) | Redis Streams |
| IdempotencyRepository | 請求去重(24 小時 TTL) | Redis |
| EventPublisher | 發布領域事件 | Kafka |
| PaymentService | 處理付款事件(遇到無效輸入回傳 `ErrInvalidPaymentEvent`,讓 consumer 能 dead-letter) | 應用層服務 |
| PaymentGateway | 扣款 | Mock(可設定成功率) |
| DistributedLock | 領導者選舉 | PostgreSQL advisory locks |
| UnitOfWork | 交易管理 | PostgreSQL |

`EventRepository.GetByID` 只做一般讀取;另外有一個獨立的 `GetByIDForUpdate` 會帶 `FOR UPDATE` row lock,必須在 UoW 管理的交易內呼叫。舊的 `DeductInventory` 方法在 remediation 階段已從介面移除(沒有 production 呼叫點)。

---

## 4. 資料庫 Schema

### PostgreSQL(對外 port 5433)

```sql
-- events: 庫存的事實來源
CREATE TABLE events (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    total_tickets INT NOT NULL,
    available_tickets INT NOT NULL,
    version INT DEFAULT 0
);
-- Migration 000004 新增:
ALTER TABLE events ADD CONSTRAINT check_available_tickets_non_negative
  CHECK (available_tickets >= 0);
-- 預設資料: INSERT INTO events (name, total_tickets, available_tickets)
--           VALUES ('Jay Chou Concert', 100, 100);

-- orders: 訂單交易紀錄
CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    event_id INT NOT NULL,
    user_id INT NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    status VARCHAR(50) NOT NULL,  -- pending, confirmed, failed, compensated
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
-- Migration 000004 加上 UNIQUE(user_id, event_id) 約束。
-- Migration 000006 改為部分唯一索引,以允許付款失敗後重試:
CREATE UNIQUE INDEX uq_orders_user_event ON orders (user_id, event_id)
  WHERE status != 'failed';

-- events_outbox: 事件發布的交易型 outbox
CREATE TABLE events_outbox (
    id SERIAL PRIMARY KEY,
    event_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(20) DEFAULT 'PENDING',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    processed_at TIMESTAMPTZ  -- migration 000005 加入
);
```

### Migration 歷史(`deploy/postgres/migrations/` 內共 7 個檔案)

| # | 內容 |
|---|------|
| 000001 | 建立 `events` 資料表 + 初始化 "Jay Chou Concert"(100 張票) |
| 000002 | 建立 `orders` 資料表 |
| 000003 | 建立 `events_outbox` 資料表 |
| 000004 | 加入 `check_available_tickets_non_negative` + `UNIQUE(user_id, event_id)` |
| 000005 | 為 `events_outbox` 加上 `processed_at` 欄位 |
| 000006 | 以部分索引 `WHERE status != 'failed'` 取代 unique constraint — 允許使用者在付款失敗後重試購買 |
| 000007 | 加入部分索引 `events_outbox_pending_idx ON events_outbox(id) WHERE processed_at IS NULL` — 加速 OutboxRelay.ListPending。使用 `CREATE INDEX CONCURRENTLY`,檔案內有 `-- golang-migrate: no-transaction` pragma |

### Redis

| Key Pattern | 型別 | 用途 |
|-------------|------|------|
| `event:{id}:qty` | String (integer) | 熱庫存計數器(30 天 TTL,讓被刪除活動的孤兒 key 最終會自然過期) |
| `orders:stream` | Stream | 非同步訂單佇列 |
| `orders:dlq` | Stream | Worker 端 DLQ(用盡 3 次重試預算的訊息) |
| `idempotency:{key}` | String | 請求去重(24 小時 TTL) |
| `saga:reverted:{order_id}` | String | 補償冪等性(7 天 TTL) |
| `saga:retry:p{partition}:o{offset}` | String (integer) | 持久化的 saga-consumer 重試計數(24 小時 TTL)— 重啟後仍能正確套用 `maxRetries=3` |

### Kafka Topics

| Topic | Producer | Consumer Group | Consumer | Payload |
|-------|----------|----------------|----------|---------|
| `order.created` | OutboxRelay | `payment-service-group`(可由 `KAFKA_PAYMENT_GROUP_ID` 覆蓋) | PaymentWorker(KafkaConsumer) | OrderCreatedEvent (id, user_id, event_id, quantity, amount) |
| `order.created.dlq` | KafkaConsumer 在遇到無法解析 / `ErrInvalidPaymentEvent` 時寫入 | — | —(未來的 DLQ worker) | 原始 payload + `x-original-{topic,partition,offset}` / `x-dlq-{reason,error}` headers |
| `order.failed` | PaymentService(透過 outbox) | `booking-saga-group`(可由 `KAFKA_SAGA_GROUP_ID` 覆蓋) | SagaCompensator(透過 SagaConsumer) | OrderFailedEvent (order_id, event_id, user_id, quantity, reason) |
| `order.failed.dlq` | SagaConsumer 在超過 `sagaMaxRetries` 之後 | — | —(未來的 DLQ worker) | 同樣的 provenance headers + reason=`max_retries` |

Consumer group 跟 topic 名稱都來自 `KafkaConfig`(`KAFKA_PAYMENT_GROUP_ID`, `KAFKA_ORDER_CREATED_TOPIC`, `KAFKA_SAGA_GROUP_ID`, `KAFKA_ORDER_FAILED_TOPIC`)。舊版的 `payment-service-group-test` 字面常數是 prod/test 命名混淆的潛在 bug,已在 remediation 中移除。

---

## 5. API 文件

### POST /api/v1/book
訂票。
```json
// Request
{ "user_id": 123, "event_id": "019dd493-47ae-79b1-b954-8e0f14a6a482", "quantity": 1 }
// Headers: Idempotency-Key: <ASCII 可印字元、<= 128 字元>(選填)

// 202 Accepted — Redis 端扣減成功;後續流程是非同步的
{
  "order_id": "019dd493-480a-7499-b208-812c930b152e",
  "status": "processing",
  "message": "booking accepted, awaiting confirmation",
  "links": { "self": "/api/v1/orders/019dd493-480a-7499-b208-812c930b152e" }
}
// 409 Conflict — 售完
{ "error": "sold out" }
// 409 Conflict — 重複下單
{ "error": "user already bought ticket" }
// 409 Conflict — Idempotency-Key 與不同的 request body 一起被重送(N4)
{ "error": "Idempotency-Key reused with a different request body" }
// 400 Bad Request — Idempotency-Key 不符 ASCII 可印字元 / 長度上限
{ "error": "Idempotency-Key must be ASCII-printable and at most 128 characters" }
// 500 Internal Server Error(已 sanitize)
{ "error": "internal server error" }
```

成功時回的是 `202 Accepted` — 對非同步管線是誠實的。Redis 端的庫存扣減成功了(load-shed gate),訂單意圖已經進佇列;DB 持久化 + 付款 + saga 還在進行中。Client 用 `order_id` 對 `GET /api/v1/orders/:id` 輪詢最終狀態。`order_id` 是 UUIDv7,在 API 邊界由 `BookingService.BookTicket` 鑄造,然後沿 Redis stream → worker `domain.NewOrder(id, ...)` → DB orders.id → outbox → Kafka order.created → payment + saga 一路串到底。PEL 重送會復用同一個 id;PR-47 之前 worker 在每次重送都鑄一個新 uuid,client 拿到的 id 會跟 DB 的 id 對不起來。

**Idempotency-Key 契約(N4)** — Stripe 風格的 fingerprint 驗證:

| 情境 | 快取狀態 | Server 回應 |
| :-- | :-- | :-- |
| 第一次帶 key X 的請求 | Miss | 正常處理;把 `(response, sha256(body))` 寫入 24h 快取 |
| 同 key X + 同 body | Hit, fingerprint 相符 | 重播快取的回應,設 `X-Idempotency-Replayed: true`。Service **不會**被呼叫。 |
| 同 key X + 不同 body | Hit, fingerprint 不符 | **409 Conflict** — **不重播**(會誤導 client 以為新請求成功)。Client 必須對新 body 用一把新的 key。 |
| 同 key X(N4 之前快取) | Hit, fingerprint 為空 | 重播 + 把新算出的 fingerprint 懶式寫回去,後續重播才能驗證。每個 key 的遷移視窗在**第一次重播時就關閉**(寫回 upgrade 該 entry);最壞情況才是 24h TTL — 那是針對「整個 24h 內都沒被重播過」的 key。 |
| 同 key X、原本回**任何 4xx** | 不快取 | 沿用 Stripe 慣例。涵蓋兩類:**驗證 4xx**(手誤的 body — 快取會讓 key 被燒掉 24h)以及**業務 4xx**(sold-out 409、duplicate 409 — 暫態業務狀態,可能在 24h TTL 內變化;快取住會阻止合法重試)。 |
| 同 key X、原本回**任何 5xx** | **不快取**(刻意偏離 Stripe) | Stripe 快取 5xx 是為了避免 client 對已降級的 payment gateway retry-storm,假設 5xx 代表「穩定的降級狀態」。我們的 5xx 大多是 transient(Redis 抖動、DB 一次性卡頓)或 programmer-error(沒被 map 過的錯誤型別)— 把這些 pin 住 24h 對客戶體驗的傷害比讓 client 在服務恢復後重試還大。retry-storm 那一面的疑慮由 nginx 邊界限流處理。**只有 2xx 會被快取** — 它是唯一一種「穩定、可重現、可以安全 replay」的 terminal outcome。 |
| 同 key X、cache GET 上游已經錯了 | Set 跳過 | 縱深防禦:flaky-then-recovered 的 Redis 寫入新回應會把可能暫態的狀態 pin 住 24h。fail-open 的可用性路徑(請求繼續處理)保留;只是跳過 cache 寫入,後續重試會打到乾淨的 cache。 |

Fingerprint 是把原始 request body bytes 做 hex `SHA-256`。**不做** JSON canonicalization — client 必須送 byte-identical 的重試(這是 Stripe / Shopify / GitHub / AWS 的事實標準)。Idempotency key 必須是 ASCII 可印字元(0x20–0x7E)、最多 128 字元;控制字元會被拒絕,避免下游的日誌解析器被混淆。重播結果(match / mismatch / legacy_match)透過 `idempotency_replays_total{outcome}` counter 暴露(見 [docs/monitoring.zh-TW.md §2](monitoring.zh-TW.md))。

錯誤回應都會通過 `api/booking/errors.go :: mapError`:透過 `errors.Is` 比對 sentinel 錯誤後,回傳一個安全的公開訊息。原始的 DB / driver 錯誤只會記錄在伺服器端(帶 correlation ID),**絕不**回給 client。

### GET /api/v1/orders/:id
用 id 輪詢一筆訂票的最終狀態。id 是 `POST /api/v1/book` 回應裡的 UUID v7。

```json
// 200 OK
{
  "id": "019dd493-480a-7499-b208-812c930b152e",
  "event_id": "019dd493-47ae-79b1-b954-8e0f14a6a482",
  "user_id": 123,
  "quantity": 1,
  "status": "confirmed",  // 或 "pending" / "charging" / "failed" / "compensated"
  "created_at": "2026-04-29T13:34:14.230Z"
}
// 404 Not Found — 見下方「非同步處理視窗」說明
{ "error": "order not found" }
// 400 Bad Request — id 參數不是合法的 UUID
{ "error": "invalid order id" }
```

**404 契約**。Worker 是非同步寫入訂單列的,在 `POST /book` 回 202 之後大概 ~ms 才完成。在這個視窗裡 `GET /orders/:id` 會回 404。Client 應該帶 backoff 重試(例如 100ms → 250ms → 500ms,試幾次)。一旦 row 存在了,後續每次 GET 都會回最新狀態。**Auth 缺口**:這個端點目前沒有驗證 — 任何人有 `order_id` 都讀得到。JWT + 擁有者檢查留待 N9 處理。

### GET /api/v1/history
分頁查詢訂單歷史。
```
?page=1&size=10&status=confirmed
```

### POST /api/v1/events
建立新活動。
```json
{ "name": "Concert", "total_tickets": 1000 }
```

### GET /api/v1/events/:id
查看活動。呼叫時會遞增 `page_views_total` 指標,用於轉換率追蹤。

### GET /metrics
Prometheus metrics 端點。

> **已移除:**舊版的 `POST /book` 路由(Phase 0 起保留至今)在 remediation 階段被刪除 — 它註冊在 `/api/v1` 群組之外,會繞過 Nginx `location /api/` 的限流區塊。所有呼叫方都必須改用 `/api/v1/book`。

---

## 6. 基礎建設模式

### 6.1 Redis Lua Scripts(原子操作)

**deduct.lua** — 扣減庫存並發布至 Stream
```
1. DECRBY event:{id}:qty by quantity
2. 若結果 < 0: INCRBY 回滾,回傳 -1(售完)
3. XADD orders:stream 並帶上訂單資料
4. 回傳 1(成功)
```

**revert.lua** — 冪等補償(改為先 INCRBY、後 SET 的順序)
```
1. EXISTS saga:reverted:{order_id} → 若已存在,回傳 0(已經補償過)
2. INCRBY event:{id}:qty
3. SET saga:reverted:{order_id} NX EX 604800(7 天)
4. 回傳 1(補償完成)
```

順序調整的理由(remediation item H6):在 `appendfsync=always` + Redis 中途崩潰的情境下,舊版的 SETNX-then-INCRBY 順序可能會把 idempotency key 持久化、卻沒有真的 INCRBY,結果所有後續重試都會因為 key 已存在而靜默跳過 — 這是一種無聲的 under-revert。新順序在同樣情境下會造成一次大聲的 over-revert(庫存 > 總張數,系統本來就會告警),遠比靜默失敗好抓。正常的 Lua 執行是原子性的,並行的呼叫者無法觀察到中間狀態。

### 6.2 Transactional Outbox

1. Worker 在同一 PostgreSQL 交易內寫入 `Order` + `OutboxEvent`
2. OutboxRelay(背景 goroutine)每 500ms 輪詢 `events_outbox WHERE processed_at IS NULL`
3. 將該批(上限 100 筆)發布至 Kafka
4. 每筆成功後標記為 processed
5. 透過 `pg_try_advisory_lock(1001)` 做領導者選舉,確保同時只有 1 個 instance 在發布

### 6.3 Saga 補償

1. PaymentWorker 消費 `order.created`,呼叫 PaymentGateway.Charge()
2. 失敗時:更新訂單狀態為 `failed`,並寫入 `order.failed` outbox 事件
3. SagaCompensator 消費 `order.failed`(透過 `SagaConsumer`):
   - DB: `IncrementTicket` + 更新訂單狀態為 `compensated`(同一交易,以 `OrderStatusCompensated` 作為冪等守門員)
   - Redis: `revert.lua` 先做 `INCRBY`,再以 `SET NX EX 7d` 記錄補償鍵 — crash-safe 的順序(見第 6.1 節)
4. **重試計數由 Redis 持久化**(`saga:retry:p{partition}:o{offset}` TTL 24 小時),consumer 重啟也不會被 reset
5. 超過 `sagaMaxRetries = 3` 次後,訊息會被寫入 `order.failed.dlq`(帶 provenance headers)、清掉計數器、commit offset,並同時遞增 `saga_poison_messages_total` 與 `dlq_messages_total{topic="order.failed.dlq", reason="max_retries"}` — 不會再有靜默 drop

付款端的 DLQ(`order.created.dlq`)採用相同機制:無法解析的 JSON 以及 `PaymentService.ProcessOrder` 回傳的 `ErrInvalidPaymentEvent` 都會被 dead-letter,而不是像舊版那樣 `return nil` 然後被靜默 commit。

### 6.4 Unit of Work

`PostgresUnitOfWork` 透過 context 注入封裝交易:
- `Do(ctx, fn)` 開啟交易、存進 context、執行 fn、commit/rollback
- Repositories 透過 `txKey` 從 context 取出交易
- 確保 Order + Outbox 寫入是原子性的

### 6.5 Worker Service

- 透過 consumer group(`orders:group`)讀取 Redis Stream(`orders:stream`)
- 啟動時會做 PEL(Pending Entries List)恢復,可應對 crash
- 重試 3 次,linear backoff(100ms * 第 n 次)
- 重試用盡後:回滾 Redis 庫存 + 移至 DLQ stream(`orders:dlq`)+ ACK
- 自我修復:遇到 NOGROUP 錯誤(例如 FLUSHALL 後)會自動重建 consumer group
- 交易內容(UnitOfWork): `DecrementTicket`(DB 雙重檢查)→ `Create Order` → `Create OutboxEvent` → COMMIT
- 每筆訊息都有指標記錄: `success`, `sold_out`, `duplicate`, `db_error` + 處理時間

### 6.6 三層冪等性

| 層級 | 機制 | 範圍 |
|------|------|------|
| API | `Idempotency-Key` header → Redis 快取(24 小時 TTL) | 重複 HTTP 請求 |
| Worker | `(user_id, event_id) WHERE status != 'failed'` 部分唯一索引 | 重複訂單(允許付款失敗後重試) |
| Saga | Redis 內 `SETNX saga:reverted:{order_id}` | 重複補償 |

### 6.7 Charging Intent Log + Reconciler(A4)

A4 在 `Pending` 與 `Confirmed`/`Failed` 之間加入 `OrderStatusCharging` 中間狀態。Payment service 在呼叫 gateway **之前** 先寫 Charging,獨立的 `recon` 子指令再透過 `PaymentStatusReader.GetStatus` 解決卡在中途的訂單。

**為什麼** — 在 gateway-side 冪等性之上的 defense-in-depth:

- **可觀測性**:`status=charging` 的列就是「正在 gateway 那邊處理中」的訂單。Stuck-Charging > 5 分鐘是可被告警的訊號(Prometheus rule `ReconStuckCharging`)。
- **跨 process 復原**:worker crash 或 Kafka rebalance 可能讓訂單卡在 Charging 但原本的 Charge call 永遠不會回來。`recon` 子指令不需要等 Kafka 重新投遞就能解決。
- **延遲指標**:`Pending → Charging → Confirmed` 的時間變成「gateway-perceived latency」直方圖(`recon_resolve_age_seconds`)。

**狀態機(A4 之後)**:

```
Pending  ──MarkCharging──→  Charging
Pending  ──MarkConfirmed─→  Confirmed   (transitional — 詳見下方 cutover)
Pending  ──MarkFailed────→  Failed      (transitional)
Charging ──MarkConfirmed─→  Confirmed   (terminal)
Charging ──MarkFailed────→  Failed
Failed   ──MarkCompensated→ Compensated (terminal)
```

**Reconciler 子指令**(`booking-cli recon`):

- 預設 loop mode:`time.Ticker` 驅動,跑到 SIGTERM 為止。適用 docker-compose / k8s Deployment 部署。
- `--once` flag:跑一次 sweep 後退出。適用 k8s CronJob 模式,排程交給 cluster orchestrator。
- 兩種模式共用同一個 `*Reconciler.Sweep(ctx)` 方法 — 邏輯不會 drift。
- `PaymentStatusReader` port(只讀,**沒有** `Charge`)— 用型別系統本身防止 recon 程式碼意外發動 double-charge。

**Per-order 結果**(counter `recon_resolved_total{outcome=...}`):

| Outcome | 觸發條件 | 動作 |
| :-- | :-- | :-- |
| `charged` | Gateway 回 `ChargeStatusCharged` | MarkConfirmed |
| `declined` | Gateway 回 `ChargeStatusDeclined` | MarkFailed |
| `not_found` | Gateway 沒有此筆紀錄(worker 在 Charge call 之前掛掉) | MarkFailed(顧客未被扣款) |
| `unknown` | Gateway 回了無法分類的 verdict | Skip,下次 sweep 再試 |
| `max_age_exceeded` | 訂單年齡 > `RECON_MAX_CHARGING_AGE`(預設 24h) | 強制 MarkFailed;告警觸發;人工 review |
| `transition_lost` | Mark* 回 `ErrInvalidTransition`(worker 贏了競賽) | 冪等成功;有計數但只記 Info log |

另一個 counter `recon_gateway_errors_total` 專門記基礎設施失敗(網路、gateway 5xx、ctx timeout)— 與 `unknown` verdict 在 operationally 是不同訊號。

**Cutover trigger** — 何時可以收緊 transitional 加寬:

目前 `MarkConfirmed` / `MarkFailed` 接受 `source ∈ {Pending, Charging}`,讓 A4 部署前還在 queue 裡的 in-flight Pending 訊息可以走原本的直接路徑。後續 PR 會在以下 **moving-window** query 連續 5 次(每分鐘跑一次)回傳 0 時收緊成 Charging-only:

```sql
SELECT count(*) FROM order_status_history
 WHERE from_status = 'pending'
   AND to_status   IN ('confirmed', 'failed')
   AND occurred_at > NOW() - INTERVAL '5 minutes';
```

window 是 5 分鐘 lookback(**不是** since deploy 的累積值)— 這個 interval 對齊 in-flight Pending 訊息最久的可能 Kafka redelivery + 重試 budget。連續 5 次回 0 = 25 分鐘內沒有 Pending→terminal 轉換 = 可以安全移除 transitional edges。

在那之前,雙來源路徑是有意的設計,不是 legacy。

**設定**(每個預設值在 [config.go](../internal/infrastructure/config/config.go) 的 header comment 裡都有 rationale;透過 `RECON_*` env var 調整):

| 旋鈕 | 預設值 | 控制什麼 |
| :-- | :-- | :-- |
| `RECON_SWEEP_INTERVAL` | 120s | Loop 節奏 |
| `RECON_CHARGING_THRESHOLD` | 120s | recon 認為訂單「卡住」的最短年齡 |
| `RECON_GATEWAY_TIMEOUT` | 10s | 每筆 GetStatus call 的 budget |
| `RECON_MAX_CHARGING_AGE` | 24h | 強制 fail 的給予放棄 cutoff |
| `RECON_BATCH_SIZE` | 100 | 每次 sweep 處理的訂單數 |

所有預設值都是 heuristic init values — 詳見 config.go 的 header comments。等實際 production-shaped run 產生 `recon_resolve_age_seconds` + `recon_gateway_get_status_duration_seconds` 直方圖之後再依資料調整。

### 6.7.1 Saga Watchdog (A5)

Saga watchdog 是 reconciler 的**對稱姊妹** — 同樣的 loop 形狀、同樣的 `--once`/loop 模式、同樣的 partial-index 策略 — 但處理的是不同的失敗面。

| Sweeper | 偵測什麼 | 解決路徑 | 索引 predicate |
| :-- | :-- | :-- | :-- |
| Reconciler(A4,[internal/application/recon](../internal/application/recon)) | 卡在 `Charging` 的訂單(worker 在 Charge 中途掛掉) | 查 payment gateway,轉成 Confirmed/Failed | `idx_orders_status_updated_at_partial WHERE status IN ('charging','pending')`(000010) |
| **Saga watchdog(A5,[internal/application/saga](../internal/application/saga))** | 卡在 `Failed` 的訂單(saga consumer 在 handler 中途掛掉、DLQ 吞掉了 event) | 重新觸發(idempotent 的)compensator | 同一個 partial index,**predicate 加上 `'failed'`**(000011) |

**為什麼重新觸發 compensator 而不是重新發到 Kafka:**

- 直接呼叫省掉每筆 stuck order 的 Kafka round-trip + offset commit。
- Compensator 的 idempotency 檢查(在 UoW closure 裡的 `order.Status() == OrderStatusCompensated`)能處理「watchdog 的 FindStuckFailed 跟重新觸發中間,saga consumer 自己跑完了」的競爭情境。
- 重發 Kafka 需要重建原本的 `event_id` + `correlation_id`;做得到但徒增複雜度,行為上沒有額外好處。

**Force-fail 策略的差異:**

- Reconciler 的 max-age 分支**會**自動 force-fail:gateway 已經告訴我們扣款狀態,我們有 ground truth 可以動作。
- Watchdog 的 max-age 分支**不會**自動轉狀態。沒驗證 Redis 庫存是否真的回復就把 Failed → Compensated 不安全(會留下 phantom-revert 狀態機污染)。Watchdog 只記 ERROR 日誌 + 觸發 `saga_watchdog_resolved_total{outcome="max_age_exceeded"}` + 觸發 `SagaMaxFailedAgeExceeded` 告警 — 由 operator 透過 `order_status_history` 人工調查。

**可調參數(env 變數)**:

| Env var | 預設 | 用途 |
| :-- | :-- | :-- |
| `SAGA_WATCHDOG_INTERVAL` | 60s | Loop 頻率(比 recon 的 120s 緊一點 — compensator 是本地呼叫、快) |
| `SAGA_STUCK_THRESHOLD` | 60s | watchdog 認為 Failed 訂單「卡住」的最短年齡 |
| `SAGA_MAX_FAILED_AGE` | 24h | 人工檢視的 give-up cutoff |
| `SAGA_BATCH_SIZE` | 100 | 每次 sweep 處理的訂單數 |

`Config.Validate()` 拒絕任何非正值,也拒絕 `MaxFailedAge ≤ StuckThreshold` 的設定(否則第一次 sweep 看到的每筆訂單都會被 force-flag) — 跟 `ReconConfig` 同樣的 cross-field guard pattern。

**Run mode**(`booking-cli saga-watchdog`):

- 預設 loop:ticker 驅動,跑到收到 SIGTERM 為止。適合 docker-compose / Deployment 部署。
- `--once`:跑一次 sweep 就退出。適合 k8s CronJob 部署,排程由編排器處理。

**範圍釐清 — A5 不處理什麼:**

A5 確保**自動補償流程能可靠地完成**。它**不處理**更深一層的設計問題:**自動補償是不是對每一種付款失敗都是正確回應?** 現在的 `OrderStatusFailed` 是個混合桶,把兩種語意不同的情況綁在一起:

| 失敗類型 | 觸發原因 | 目前處理方式 | 比較合理的處理方式 |
| :-- | :-- | :-- | :-- |
| **業務失敗** | 信用卡被拒、餘額不足、3DS 驗證未通過 | 自動補償(回復庫存,MarkCompensated)。沒通知使用者,同個訂單不能重試。 | 把失敗原因告訴使用者;允許他用另一張卡對**同一個保留庫存**重試。 |
| **服務失敗** | Gateway 5xx、網路 timeout、我們自己服務有 bug | 跟業務失敗同樣自動補償 — **沒驗證 gateway 實際上有沒有扣款** | 補償前先驗證 gateway 狀態(呼叫 `gateway.GetStatus`)。如果結果不確定,隔離起來給 operator 人工檢視(避免 phantom-charge 風險)。 |

`OrderFailedEvent` 在 `Reason` 欄位帶了失敗原因(把 gateway 呼叫拿到的 `err.Error()` 塞進去)— 但這個 reason **從來沒被寫進 orders 資料表** — saga compensator 消費完 event payload 就丟了。所以資料庫裡完全沒記載某張 `compensated` 訂單**為什麼**會變成這樣;要查只能去翻 Kafka 日誌。

A5 在現在這個「單一桶」的模型裡是對的。語意重構(把 `failed_reason` 寫進去、依不同原因走不同路徑、把業務失敗推給使用者)是好幾週的產品工作,記在 [`architectural_backlog.md §13`](../architectural_backlog.md)。等那個工作落地,A5 的契約就會收斂成「只處理服務失敗端的恢復」 — 不需要改 A5 的程式碼。

### 6.8 Redis Streams Hardening

booking pipeline 用兩個 Redis Streams:`orders:stream`(熱資料佇列,API → worker)和 `orders:dlq`(等待 operator 檢視的失敗訊息)。本次同時上線三個觀測性 + 保留期相關問題的修正:

**每個 stream 的容量策略** — 故意不對稱:

| Stream | 上限策略 | 原因 |
| :-- | :-- | :-- |
| `orders:stream` | **不設上限** | 每筆都是顧客訂單。`MAXLEN` 會默默丟掉最舊的未處理訂單 → 靜默資料遺失 → 災難級。透過分級告警 +(未來)生產者端的反壓來界定有限增長。 |
| `orders:dlq` | 每次 XADD 帶 **`MINID ~ <NOW − REDIS_DLQ_RETENTION>`**(預設 30d) | DLQ 裡是已經失敗、等待 operator 檢視的訊息。過了保留期窗口後不是被修就是被 write-off;以時間為基礎的淘汰是有界的保留期,不會像 in-flight 訊息那樣被靜默丟掉。可透過 `REDIS_DLQ_RETENTION`(`config.RedisConfig.DLQRetention`)設定;`Validate()` 會拒絕 ≤ 0 的值,因為 0 會在每次 XADD 都把全部 entry 砍掉。未來:在 MINID 把它們淘汰前先封存到 S3。 |

**Streams observability** ([streams_collector.go](../internal/infrastructure/observability/streams_collector.go)) — `prometheus.Collector`,在 scrape 時讀 XLEN + XPENDING summary:

| 指標 | 來源 | 用途 |
| :-- | :-- | :-- |
| `redis_stream_length{stream}` | `XLEN`(O(1)) | 熱 stream 應該排空到 ~0;持續 > 0 = 積壓 |
| `redis_stream_pending_entries{stream,group}` | `XPENDING` summary 計數 | In-flight 工作(已派發但還沒 XACK) |
| `redis_stream_consumer_lag_seconds{stream,group}` | `NOW() − parse_ms(XPENDING.Lower)` | 最舊 pending 條目的年齡 — Redis Streams 的標準 lag 訊號 |

成本:每個 stream 每次 scrape 兩個 Redis round-trip(XLEN + XPENDING summary)。Prometheus 預設 15s scrape × 2 個 stream = ~0.27 calls/sec。微不足道。

**`orders:stream` 的分級告警**:

| 告警 | 門檻 | severity | 動作 |
| :-- | :-- | :-- | :-- |
| `OrdersStreamBacklogYellow` | 長度 > 10K 持續 2m | info | 「在 tier 2 之前先調查」 |
| `OrdersStreamBacklogOrange` | 長度 > 50K 持續 2m | warning | 「立即 page on-call」 |
| `OrdersStreamBacklogRed` | 長度 > 200K 持續 1m | critical | 「OOM 即將發生 — 手動 scale 或限流」 |
| `OrdersStreamConsumerLag` | lag > 60s 持續 2m | warning | 「特定 consumer 卡住(GC、hung syscall)」 |
| `OrdersDLQNonEmpty` | DLQ 長度 > 0 持續 5m | warning | 「operator 用 XRANGE orders:dlq 檢視」 |

**HTTP 邊界的 request body 大小上限** ([api/middleware/body_size.go](../internal/infrastructure/api/middleware/body_size.go)):

大小驗證放在 HTTP 層,**不在**快取裡(業界慣例 — Stripe / Shopify / GitHub Octokit / AWS API Gateway)。`BodySize(MaxBookingBodyBytes)` 包住 `/api/v1` group:`MaxBookingBodyBytes = 16 KiB`,以 `http.MaxBytesReader` 強制執行(同時擋下宣告的 `Content-Length` 超量、以及 chunked body 在實際讀取時的 overflow)。Oversize 的請求會收到 **413 Payload Too Large**,body 是標準的 `dto.ErrorResponse` 形狀;handler 永遠不會被叫到。

為什麼是 16 KiB 而不是 Stripe 的 1 MB:booking 端點吃的是固定形狀 JSON(實際約 80 bytes)。上限要抓緊 — 寬鬆的上限會放大「合法請求 vs 攻擊請求」的比例。下游的快取層因此可以信任已經驗證過的輸入;`Idempotency-Key` 對應的 value 不可能比產生它的 body 還大。

**延後到後續 PR 處理**:

- **生產者端反壓**:`XLEN > threshold` → booking handler 回 503。把 queue 限定在有界範圍,代價是顯式拒絕。threshold 需要 k6 + worker-killed 負載資料才能調(等 N6 test infra 完成後)。
- **Redis 8 + DLQ XAdd 用 IDMP**:Redis 8 內建 server-side stream-entry idempotency(`XADD ... IDMP <token>`),消除 worker-retry-after-XACK-failure 造成的重複 DLQ 條目。需要先升級到 Redis 8(目前在 7-alpine)。
- **booking-side IDMP**:優先序較低,因為 HTTP 層的冪等快取已經能阻擋使用者可見的重複下單。

---

## 7. 可觀測性

### 指標(Prometheus)
| 指標 | 型別 | 說明 |
|------|------|------|
| `http_requests_total` | Counter | 依 method/path/status 統計請求量(path 使用 Gin 路由模板,cardinality 有界) |
| `http_request_duration_seconds` | Histogram | 請求延遲(針對 p99 調整的 bucket) |
| `bookings_total` | Counter | 訂票結果(`success`, `sold_out`, `duplicate`, `error`)— 啟動時預先初始化 |
| `worker_orders_total` | Counter | Worker 處理結果(`success`, `sold_out`, `duplicate`, `db_error`)— 預先初始化 |
| `worker_processing_duration_seconds` | Histogram | Worker 延遲 |
| `inventory_conflicts_total` | Counter | Redis 通過但 DB 拒絕的超賣偵測 |
| `page_views_total` | Counter | 活動頁面瀏覽量(轉換漏斗) |
| `dlq_messages_total` | Counter(`topic`, `reason`) | 寫入 DLQ 的訊息數。預先初始化的 label 涵蓋 `order.created.dlq` 與 `order.failed.dlq`,reason 包含 `invalid_payload`、`invalid_event`、`max_retries` |
| `saga_poison_messages_total` | Counter | Saga 事件在超過 `sagaMaxRetries` 後被 dead-letter 的次數 |
| `kafka_consumer_retry_total` | Counter(`topic`, `reason`) | 因為下游短暫錯誤而故意**不 commit**、留給 Kafka rebalance 重送的訊息數。故意**不**進 DLQ(那會在 DB 小抖動時誤傷已經付款的訂單,造成超賣)。配套的 `KafkaConsumerStuck` 告警會監控這個指標 — 持續非零率就代表某個下游依賴正在降級 |
| `db_rollback_failures_total` | Counter | `tx.Rollback()` 回傳非 `sql.ErrTxDone` 的錯誤。`ErrTxDone` 是預期狀態(驅動在遇到 fatal error 後已自行關閉 tx),在呼叫端會過濾掉;其他類型的 rollback 失敗代表 tx 可能還掛著或連線被污染 |
| `redis_xack_failures_total` | Counter | Redis `XAck` 對已成功處理的訊息失敗 — 訊息會留在 PEL 等下次重送。這個計數器是「可能發生重複處理」的唯一先行訊號 |
| `redis_xadd_failures_total` | Counter(`stream`) | Redis `XAdd` 失敗,依目標 stream 分 label。目前只有 DLQ stream(`stream="dlq"`)會從 Go 寫入;label 保留是為了未來擴充主 stream 寫入者 |
| `redis_revert_failures_total` | Counter | Worker `handleFailure` 呼叫 `RevertInventory` 失敗 — 訊息留在 PEL 等下次 PEL reclaim 重試。非零速率代表 Redis 庫存正在跟 DB 產生漂移 |

**告警(`deploy/prometheus/alerts.yml`):**

- `HighErrorRate` — HTTP 5xx 比例 5m 內超過 5%(加 2m `for` 防抖動)
- `HighLatency` — p99 請求延遲 > 2s
- `InventorySoldOut` — `increase(bookings_total{status="sold_out"}[5m]) > 0`。舊版的 `booking_sold_out_total` 表達式指向一個程式碼裡不存在的指標,所以告警原本永遠不會觸發,在 remediation 中修掉。
- `KafkaConsumerStuck` — `sum by (topic) (rate(kafka_consumer_retry_total[5m])) > 1`,持續 2m。與 `kafka_consumer_retry_total` 計數器配對使用的契約:當下游短暫錯誤造成持續 rebalance retry 時,這個告警會觸發,讓 oncall 去查**下游基礎設施**(DB / Redis / payment gateway),**不是** consumer 本身。Consumer 是按設計運作的;告警存在的目的是讓「卡住但沒死」這個狀態能被 operator 看到,而不需要靠 dead-letter 正在處理中的訂單來製造可見度。

### 分散式追蹤(OpenTelemetry + Jaeger)
- Decorator 模式:`BookingServiceTracingDecorator`, `MessageProcessorMetricsDecorator`, `OutboxRelayTracingDecorator`
- `OutboxRelayTracingDecorator` 現在會在批次失敗時呼叫 `span.RecordError` + `span.SetStatus(codes.Error)` — 舊版 span 永遠是 OK
- `api/booking/handler_tracing.go` 用共用的 `recordHTTPResult(span, status)` helper,對**所有** status >= 400 都會 set `span.status = Error`(不只是 5xx),這樣 4xx client 錯誤也能在 Jaeger 搜尋中浮現
- GRPC exporter 連至 Jaeger(port 4317)
- **採樣器可設定**:透過 `OTEL_TRACES_SAMPLER_RATIO`:空/1 → AlwaysSample(預設)、0 → NeverSample、0 < r < 1 → TraceIDRatioBased(r)。若值無法解析,會 log warning 並 fallback 到 AlwaysSample(絕不靜默關閉 tracing)
- `initTracer` 現在會**fail fast**(回傳錯誤給 fx.Invoke),不再讓 `resource.New` 或 `otlptracegrpc.New` 的錯誤落到下一步,導致 nil `traceExporter` 在送第一個 span 時 crash

### 日誌(internal/log + zap)
- Structured JSON 輸出至 stdout(ISO8601 時間、`level`/`time`/`msg`/`caller` 欄位)
- **兩種使用風格**,皆記錄在 `internal/log/doc.go`:
  - **Pattern A** — 長生命週期元件(sagaCompensator、workerService、paymentService、event_service、redisOrderQueue、KafkaConsumer、SagaConsumer、OutboxRelay)使用結構體 DI logger(`s.log *mlog.Logger`),在建構時透過 `With()` 烘入 `component=<subsystem>`,讓每條 log 都能依子系統篩選。
  - **Pattern B** — HTTP handlers、middleware、init 程式碼沒有穩定的 component 身份,使用套件層級的 ctx-aware 呼叫(`log.Error(ctx, ...)`)。
- **每次呼叫自動補上的欄位**(由 `enrichFields` 前置):
  - `correlation_id` 取自 context(由 `middleware.Combined` 注入)
  - `trace_id` / `span_id` 取自 `trace.SpanContextFromContext(ctx)` — ctx 內有 OTEL span 時自動出現,呼叫端完全不需改動
- **每個 request 不再 clone zap core** — middleware 以一次 `context.WithValue` 存 `{logger, correlationID}` 值結構。happy path 不為 logger 狀態分配任何記憶體。
- **執行期 level 切換**:`GET`/`POST` `/admin/loglevel` 掛在 pprof listener 上,直接切換 `AtomicLevel`,不用重啟(同樣受 `ENABLE_PPROF=true` 控制)
- **型別化欄位建構子** 放在 `internal/log/tag/`(`tag.OrderID(id)`、`tag.Error(err)` 等)— 熱路徑上享有編譯期打錯防護

### 儀表板(Grafana)
預先配置的 6 格儀表板: RPS、Latency Quantiles、Conversion Rate、IP Fairness、Saturation

### Profiling(pprof)
- `net/http/pprof` 把 `/debug/pprof/*` 開在**獨立的** `:6060` listener — 不走主要 Gin router,也不經過 nginx
- 由 `ENABLE_PPROF` 環境變數控制(設 `true` 開啟,預設 `false`)。`docker-compose.yml` 僅為本機使用 publish 6060
- 以 `http.Server` 包起,並配 fx `OnStop` hook(clean shutdown,無 goroutine leak)
- 擷取腳本:`scripts/pprof_capture.sh` 在壓測中抓 heap + allocs(30 秒取樣)+ goroutine profile;`scripts/benchmark_gc.sh` 負責整個 run 的編排
- 分析熱點分配用 `go tool pprof -alloc_space -top pprof/heap.pb.gz`

### Runtime 調優環境變數

Go runtime + OTel + pprof 開關。透過 `.env` 提供本機開發預設值,`docker-compose.yml` 引用。

| 變數 | 預設(.env) | Fallback(compose) | 用途 |
|------|--------------|---------------------|------|
| `GOGC` | `400` | `100` | GC 觸發比例,越大 GC 越少、peak heap 越高 |
| `GOMEMLIMIT` | `256MiB` | (未設) | 軟記憶體上限,搭配 GOGC 讓 GC 只在接近上限時變積極 |
| `OTEL_TRACES_SAMPLER_RATIO` | `0.01` | `1` | 採樣比例。`0` 全關,`1` 全開 |
| `ENABLE_PPROF` | `true` | `false` | 是否啟動 pprof listener(位址由 `PPROF_ADDR` 控制,預設 `127.0.0.1:6060`) |

### Config 覆寫變數(yaml + env,PR #21 / #22 後新增)

以下 env vars 覆寫 `config/config.yml` 裡同名的 key。cleanenv 合併順序:env-default → yaml → env(env 優先)。booking-cli review cleanup(PR #21 / #22)把這些調整項從 const 改搬到 config,之後不用重 build 就能改。

| 變數 | yaml key | 預設 | 用途 |
|------|----------|------|------|
| `CONFIG_PATH` | —(bootstrap) | `config/config.yml` | Config 檔路徑。讓 systemd / k8s initContainer 可以使用非 CWD 的位置 |
| `PPROF_ADDR` | `server.pprof_addr` | `127.0.0.1:6060` | pprof listener 綁定位址。**預設綁 loopback** — heap dump 與 `/admin/loglevel` 不得在沒有明確覆寫的情況下對外可達 |
| `PPROF_READ_TIMEOUT` | `server.pprof_read_timeout` | `5s` | pprof listener 的 read deadline |
| `PPROF_WRITE_TIMEOUT` | `server.pprof_write_timeout` | `30s` | 大 heap dump 可能超過預設的 5s |
| `TRUSTED_PROXIES` | `server.trusted_proxies` | RFC1918 CIDR | Gin 做 `ClientIP()` 解析時信任的 CIDR。env 用逗號分隔;yaml 是 sequence。要用在 RFC1918 以外的 service mesh(GKE、部分 EKS 設定)時覆寫 |
| `DB_PING_ATTEMPTS` | `postgres.ping_attempts` | `10` | DB 啟動探測重試次數。k8s initContainer / 依賴服務較慢時要拉高 |
| `DB_PING_INTERVAL` | `postgres.ping_interval` | `1s` | 兩次 DB ping 之間的間隔 |
| `DB_PING_PER_ATTEMPT` | `postgres.ping_per_attempt` | `3s` | 單次探測的 context timeout |
| `KAFKA_BROKERS` | `kafka.brokers` | `localhost:9092` | **PR #22 型別變更**:`Brokers` 改為 `[]string`(cleanenv `env-separator:","`)。env 用逗號分隔;yaml 是 sequence。先前 `[]string{cfg.Brokers}` 把整個逗號字串當成單一位址 — multi-broker 設定先前其實靜默失效 |

---

## 8. 開發階段(完整歷史)

| Phase | 日期 | Commit / PR | 內容 |
|-------|------|-------------|------|
| 0 | 2/14 | `65502bb` | 基本訂票 API + Postgres + Prometheus/Grafana/Jaeger + CLI |
| 1 | 2/15 | `67234b4`, `f9ff381` | K6 壓測 + scaling roadmap + 基準測試自動化 |
| 2 | 2/15 | `65058a9` | Redis 熱庫存(Lua scripts,4k → 11k RPS) |
| 3 | 2/16 | `fefa372` | 集中式 YAML config + Lua script 強化 + HTTP 409 |
| 4 | 2/17 | `1e80723` | X-Correlation-ID middleware + panic recovery |
| 5 | 2/17 | `96d8f51` | Redis Streams 非同步佇列 + WorkerService + ADR-001 |
| 6 | 2/17 | `df38baa` | 冪等性(API + worker) + Unit of Work 模式 |
| 7 | 2/18 | `9bd9b2b` | Kafka + outbox pattern(約 8% 可靠性成本) |
| 8 | 2/18 | `51cdeb5` | Payment service(mock gateway) + E2E 流程驗證 |
| 9 | 2/19 | `1caa7a1`, `a966f45` | Worker 參數化 + 完整單元測試 |
| 10 | 2/20 | `572d430` | Nginx API gateway + 限流 + 可觀測性調整 |
| 11 | 2/21 | `f56ab82` | PostgreSQL advisory lock(OutboxRelay 領導者選舉) |
| 12 | 2/24 | `4e89ff7` | Saga 補償 + 冪等 Redis 回滾 + 部分唯一索引(允許付款失敗後重試) |
| 13 | 4/11 | PRs #7 / #8 / #9 / #12 / #13 | **多 agent review 與 remediation**:在 6 個 review 面向(domain/app、persistence、concurrency/cache、messaging/saga、api/payment、observability/deploy)共彙整出 66 項 findings。所有 6 個 CRITICAL 與 13 個 HIGH 在 [`fix/review-critical` (#8)](https://github.com/Leon180/booking_monitor/pull/8) 與 [`fix/review-high` (#9)](https://github.com/Leon180/booking_monitor/pull/9) 修復;17 MEDIUM / 14 LOW / 6 NIT 在 [`fix/review-backlog` (#12)](https://github.com/Leon180/booking_monitor/pull/12) 修復;docs + `kafka_consumer_retry_total` 指標 + `KafkaConsumerStuck` 告警 + [`docs/reviews/SMOKE_TEST_PLAN.md`](reviews/SMOKE_TEST_PLAN.md) 於 [`fix/review-docs` (#13)](https://github.com/Leon180/booking_monitor/pull/13) 完成。彙整後的 backlog 存放在 [`docs/reviews/ACTION_LIST.md`](reviews/ACTION_LIST.md)。使用者面向的改動請看下方的 **Remediation 重點** 區塊。 |
| 14 | 4/12–13 | PRs #14 / #15 | **GC 優化**:baseline benchmark 發現 RPS 比歷史掉 70%,根因是 fx.Decorate 修好後 tracing/metrics decorator 真的被套用,加上 `AlwaysSample()` 與每個 request 都 clone 一次 zap core。分兩個 PR 修復。[`perf/gc-baseline` (#14)](https://github.com/Leon180/booking_monitor/pull/14) 先建 benchmark harness(pprof endpoint 開在 `:6060`、`scripts/benchmark_gc.sh`、`scripts/gc_metrics.sh`、`scripts/pprof_capture.sh`)並導入三個 quick win(`OTEL_TRACES_SAMPLER_RATIO=0.01`、`GOGC=400`、CorrelationIDMiddleware 不再 clone zap core)— RPS 7,984 → 20,552(+157%)。[`perf/gc-deep-fixes` (#15)](https://github.com/Leon180/booking_monitor/pull/15) 緊接做 deep fix:Redis Lua script args 改用 `sync.Pool`、key 改用 `strconv.Itoa` 串接(取代 `fmt.Sprintf` boxing)、`GOMEMLIMIT=256MiB`、以及合併後的 `middleware.Combined`,每個 request 只做 1 次 `context.WithValue` + 1 次 `c.Request.WithContext` — 每 60 秒分配物件數 258M → 110M(−57%),GC 週期 202 → 86(−57%)。詳細請看下方的 **Phase 14 重點** 區塊。 |
| 15 | 4/23–24 | PR #18 | **Logger 架構重構**:`pkg/logger/` → `internal/log/`,改採 ctx-aware emit API。Middleware 不再在每個 request 呼叫 `baseLogger.With(tag.CorrelationID(id))`(該呼叫會深度 clone zap 內部 core,約 1.2 KB/req)。改成 `middleware.Combined` 以一次 `context.WithValue` 存 `ctxValue{logger, correlationID string}`;`Logger.Error(ctx, msg, fields...)` 與套件層級的 `log.Error(ctx, ...)` 由 `enrichFields` 在實際 emit 時從 ctx 讀出 `correlation_id` 與 OTEL `trace_id`/`span_id`。新增 `LevelHandler()`,把 `GET`/`POST` `/admin/loglevel` 掛在 pprof listener 上做執行期 level 切換;`ParseLevel` 對打錯的 level 直接回報錯誤(不再靜默 fallback 成 info);另新增 `internal/log/tag/` 套件提供型別化的 `zap.Field` 建構子。每個長生命週期元件(sagaCompensator、workerService、paymentService、event_service、redisOrderQueue、KafkaConsumer、SagaConsumer、OutboxRelay)現在都在注入的 logger 上烘入 `component=<subsystem>`,讓 Loki/Grafana 的標籤篩選在所有子系統上一致。詳細請看下方的 **Phase 15 重點** 區塊。 |

### Remediation 重點(Phase 13)

- **Kafka DLQ 全鏈路**:新增 topics `order.created.dlq` / `order.failed.dlq`、新增指標 `dlq_messages_total` / `saga_poison_messages_total`、saga 重試計數改為 Redis 持久化、新增 `ErrInvalidPaymentEvent` sentinel。從此不會再有靜默 drop 的訊息。
- **API 安全**:`r.Run()` 改為顯式建立的 `http.Server{}`,真正套用 `cfg.Server.ReadTimeout`/`WriteTimeout`;`api/booking/errors.go :: mapError` 對每一個錯誤回應做 sanitize,DB / driver 錯誤絕不會外洩;舊版 `POST /book` 已移除。
- **Secrets 搬到 `.env`**:所有明文密碼(`postgres`、`grafana`、`redis`)都改從 gitignore 的 `.env` 透過 `${VAR}` 取代,並提供追蹤中的 `.env.example`;docker-compose 在缺少必要值時會 fail fast。
- **`Config.Validate()`** 會拒絕缺少的 `DATABASE_URL`,並在 `APP_ENV=production` 時禁止 `REDIS_ADDR` / `KAFKA_BROKERS` 使用 localhost 預設。
- **Deploy 強化**:六個原本未 pin 的 image 全部 pin 住(`golang:1.24-alpine`、`alpine:3.20`、`nginx:1.27-alpine`、`prom/prometheus:v2.54.1`、`grafana/grafana:11.2.2`、`jaegertracing/all-in-one:1.60`);Dockerfile runner stage 改以 non-root `uid:10001` 執行;Redis 加上 `--requirepass`。
- **可觀測性**:OTel 採樣器可透過 `OTEL_TRACES_SAMPLER_RATIO` 設定;`recordHTTPResult` helper 會把 4xx 也標成 span error;`InventorySoldOut` 告警改用真實存在的 `bookings_total{status="sold_out"}` 指標。
- **Persistence**:新增部分索引 `events_outbox_pending_idx`(migration 000007);pool 設定移到 ping 迴圈之前並加上 `ConnMaxLifetime`;`GetByID` 拆成一般版 + `GetByIDForUpdate`;19 處 repository 的錯誤都改用 `%w` wrap。

### Phase 14 重點(GC 優化)

- **Benchmark harness**:`net/http/pprof` 獨立跑在 `:6060` listener(透過 `ENABLE_PPROF=true` 控制);`scripts/benchmark_gc.sh` / `scripts/gc_metrics.sh` / `scripts/pprof_capture.sh` 把 k6、Go runtime 指標、heap/allocs profile 整合成單一報告,產出在 `docs/benchmarks/`。該 listener 有自己的 `http.Server` 與 fx `OnStop` shutdown hook — 無 goroutine leak。
- **Sampler 調優**:`OTEL_TRACES_SAMPLER_RATIO` 預設 `0.01`(1%)。未被採樣的 request 只拿到一個 no-op span(零分配),不會走完整的 batch span processor export。
- **Runtime 調優**:`GOGC=400` + `GOMEMLIMIT=256MiB` — 正常流量下 GC 很鬆,heap 接近 soft limit 時才變積極,避免流量 spike 時 heap 無限成長。
- **Hot-path 分配削減**:`middleware.Combined` 把已綁定 correlation id 的 request 級 logger 一次塞進 context(`internal/log/context.go`),每個 request 只做 1 次 `context.WithValue` + 1 次 `c.Request.WithContext`;Redis Lua script args 改用 `sync.Pool` 複用 `[]interface{}`;庫存 key 改用 `strconv.Itoa` 串接(取代 `fmt.Sprintf`)避免 interface boxing;用 sentinel error(`errDeductScriptNotFound`、`errRevertScriptNotFound`、`errUnexpectedLuaResult`)取代每次呼叫都 `fmt.Errorf`。
- **結果**:clean run RPS 7,984 → 20,552(+157%);每 60 秒分配物件數 258M → 110M(−57%);GC 週期 202 → 86(−57%);GC pause 最大值 79ms → 41ms(−48%);heap peak 被 `GOMEMLIMIT` 控制在 ≤256MB。

### Phase 15 重點(Logger 重構)

- **套件搬家**:`pkg/logger/` → `internal/log/`。服務專屬的 logger 不應放在 `pkg/`(Go layout 慣例:`pkg/` = 跨 module 可重用,`internal/` = service-private)。
- **Ctx-aware emit API**:`Logger.Debug/Info/Warn/Error/Fatal(ctx, msg, fields...)` 以及套件層級的 `log.Error(ctx, ...)` 等。先呼叫 `Check()`,level 被關掉時花費**零 allocation**;啟用時 `enrichFields(ctx, user)` 會前置 `correlation_id`(從 ctx 取)加上 OTEL `trace_id` / `span_id`(從 `trace.SpanContextFromContext` 取)。每個 request 不再 clone `baseLogger.With(...)`。
- **混合使用慣例**(記錄於 `internal/log/doc.go`):Pattern A — 有穩定身份的長生命週期元件在建構時一次以 `With()` 烘入 `component=<subsystem>` 標籤,並由 fx 注入;Pattern B — 呼叫點專用的程式(handlers、middleware、init)使用套件層級 `log.Error(ctx, ...)`。兩者並存,不是不一致。
- **執行期 level 切換**:`LevelHandler()` 在 pprof listener 上提供 `GET`/`POST` `/admin/loglevel`,不需要重啟就能切換原子 level。同樣受 `ENABLE_PPROF=true` 控制。
- **型別化 tag**:`internal/log/tag/` 提供 `tag.OrderID`、`tag.EventID`、`tag.Error` 等 11 個標準 key 的 `zap.Field` 建構子 — 熱路徑上有編譯期打錯防護。
- **Caller 正確性**:兩條 emit 路徑都會回報使用者的 file:line,不會指向 `internal/log/log.go`。由 `TestCallerFrame_Method` + `TestCallerFrame_PackageLevel` 兩個 regression test 守護。
- **取捨**:同一 Docker 環境背靠背 benchmark 相比 PR #15 main 掉約 9% RPS — wrapper 派送 + `AddCallerSkip` 的 frame 巡查 + `enrichFields` 的 fast-path check。交換到的是「每 request 零 clone」、「trace 自動 enrich」與擁有自有 `Logger` 型別(未來遷移到 slog 時只需改 `log.go`,不會波及 60+ 個呼叫點)。

---

## 9. 效能基準

| 設定 | RPS | P99 / P95 延遲 | 備註 |
|------|-----|----------------|------|
| Stage 1:純 Postgres | ~4,000 | ~500ms(P99)| DB CPU 瓶頸(212%) |
| Stage 2:Redis 熱庫存 | ~11,000 | ~50ms(P99)| 記憶體/網路瓶頸 |
| Stage 2 + Kafka outbox | ~9,000 | ~100ms(P99)| Kafka 吞吐量瓶頸 |
| 完整系統(Phase 13 前) | ~26,879 | ~33ms(P95)| 2026-02 baseline — tracing decorator 因 fx bug 靜默被關掉 |
| Remediation 後、GC 優化前 | ~7,984 | ~98ms(P95)| Phase 13 修好 fx 後 decorator 真的啟用 + AlwaysSample → 掉 70% |
| **Phase 14 GC 優化後** | **~20,552** | **~45ms(P95)**| PR #14 quick wins(sampler 0.01 + GOGC=400 + 不 clone 的 middleware) |

基準測試報告在 `docs/benchmarks/` — 分成 `*_compare_c500` 的 clean run 與帶 pprof + GC metrics 的 `*_gc_*` 目錄。

---

## 10. 待辦路線圖

### 高優先
- **DLQ Worker**:建立一個後續的 consumer,以較慢的頻率消化四個 dead-letter 目的地,並在放棄之前套用可設定 backoff 的重試政策。DLQ 的**生產端**(Worker `orders:dlq`、KafkaConsumer `order.created.dlq`、SagaConsumer `order.failed.dlq`)都已經完成,並會帶齊 provenance headers(topic / partition / offset / reason / error);缺的是讀取端以及一個手動 replay 的 CLI 工具。

### 中優先
- **Event Sourcing**:把直接的 DB mutation 換成 append-only event store,獲得完整 audit trail 與 replay 能力。
- **CQRS 讀取模型**:針對查詢情境(history、分析)獨立出讀取投影。
- **水平擴展測試**:驗證多實例部署在 Nginx 負載均衡下的行為,確認 advisory lock 的領導者選舉跨 instance 仍然正確。

### 低優先
- **真實支付閘道**:以 Stripe/PayPal 取代 mock,處理 webhook。
- **管理後台**:提供活動、訂單、系統狀態的 UI。
- **Stage 4 Sharding**:跨地理區域的分散式 DB sharding。

---

## 11. 檔案參考

### 進入點
| 檔案 | 用途 |
|------|------|
| `cmd/booking-cli/main.go` | Cobra root + 子指令註冊 + `resolveConfigPath` |
| `cmd/booking-cli/server.go` | `server` 子指令:HTTP + pprof + worker + saga consumer 生命週期 |
| `cmd/booking-cli/payment.go` | `payment` 子指令:Kafka `order.created` consumer 生命週期 |
| `cmd/booking-cli/stress.go` | `stress` 子指令:一次性壓測產生器 |
| `cmd/booking-cli/tracer.go` | OTel tracer 初始化 + `OTEL_TRACES_SAMPLER_RATIO` 解析(`server` + `payment` 共用) |
| `internal/bootstrap/module.go` | `CommonModule(cfg)` — 每個子指令都需要的 log + config + DB + 基礎 observability wiring |
| `internal/bootstrap/db.go` | `provideDB`(retry-until-reachable Postgres pool)+ `registerDBPoolCollector` |
| `internal/bootstrap/logmodule.go` | `LogModule` — ctx-aware `*log.Logger` 的 fx provider |

### Domain
| 檔案 | 用途 |
|------|------|
| `internal/domain/event.go` | Event entity + OutboxEvent + Kafka 事件型別 |
| `internal/domain/order.go` | Order entity + 狀態常數 |
| `internal/domain/repositories.go` | Repository 介面 |
| `internal/domain/inventory.go` | InventoryRepository 介面 |
| `internal/domain/queue.go` | OrderQueue 介面 |
| `internal/domain/messaging.go` | EventPublisher 介面 |
| `internal/domain/payment.go` | PaymentGateway 介面(真正的領域 port — 外部整合邊界) |
| `internal/application/payment_service.go` | PaymentService 介面 + ErrInvalidPaymentEvent(PR #38 從 domain 搬過來 — 接受 `*OrderCreatedEvent`,屬於應用層的 wire DTO) |
| `internal/domain/lock.go` | DistributedLock 介面 |
| `internal/domain/idempotency.go` | IdempotencyRepository 介面 |
| `internal/domain/uow.go` | UnitOfWork 介面 |

### Application Services
| 檔案 | 用途 |
|------|------|
| `internal/application/booking_service.go` | 訂票主邏輯(Redis 扣減) |
| `internal/application/worker_service.go` | Queue 生命週期(EnsureGroup、Subscribe、ctx 處理);每則訊息的實際處理委派給已被 decorate 的 MessageProcessor |
| `internal/application/message_processor.go` | `MessageProcessor` 介面 + 基底實作(DB 交易:DecrementTicket → orderRepo.Create → outbox.Create)。從 worker_service 拆出來,讓 metrics / tracing 可以作為真正的 decorator 疊在外面 |
| `internal/application/message_processor_metrics.go` | Metrics decorator:透過 `errors.Is` 分類錯誤,發送 `worker_orders_total` / `worker_processing_duration_seconds` / `inventory_conflicts_total` 指標 |
| `internal/application/outbox_relay.go` | Outbox 發布至 Kafka |
| `internal/application/saga_compensator.go` | 付款失敗補償 |
| `internal/application/payment/service.go` | 付款處理邏輯 |
| `internal/application/worker_metrics.go` | WorkerMetrics port |
| `internal/application/booking_metrics.go` | BookingMetrics port |
| `internal/application/db_metrics.go` | DBMetrics port(rollback 失敗計數器) |
| `internal/application/queue_metrics.go` | QueueMetrics port(XAck / XAdd / Revert 失敗計數器) |

### Infrastructure
| 檔案 | 用途 |
|------|------|
| `internal/infrastructure/api/module.go` | 組合 `booking.Module` + `ops.Module`,讓 `cmd/booking-cli/server.go` 一個 fx import 就可以接上整個 HTTP boundary |
| `internal/infrastructure/api/booking/handler.go` | 顧客端 HTTP handler(`POST /book`、`GET /history`、`POST /events`、`GET /events/:id`)+ 路由註冊在 `/api/v1` 之下 |
| `internal/infrastructure/api/booking/errors.go` | `mapError(err) (status, publicMsg)` helper — booking 端點專用的公開錯誤訊息 sanitize |
| `internal/infrastructure/api/booking/handler_tracing.go` | Tracing decorator + `recordHTTPResult` span helper(4xx + 5xx 都會 set `span.status = Error`) |
| `internal/infrastructure/api/ops/health.go` | k8s `/livez` + `/readyz` 探針 — process-up vs 依賴-up,掛在 engine root(**不**在 `/api/v1` 之下) |
| `internal/infrastructure/api/middleware/middleware.go` | `middleware.Combined`(Phase 14):一次性注入 logger + correlation ID(每個 request 只 1 次 `context.WithValue` + 1 次 `c.Request.WithContext`) |
| `internal/infrastructure/cache/redis.go` | Redis inventory + idempotency repos |
| `internal/infrastructure/cache/redis_queue.go` | Redis Streams consumer |
| `internal/infrastructure/cache/lua/deduct.lua` | 原子扣減 Lua script |
| `internal/infrastructure/cache/lua/revert.lua` | 冪等補償 Lua script |
| `internal/infrastructure/persistence/postgres/repositories.go` | DB repositories |
| `internal/infrastructure/persistence/postgres/uow.go` | Unit of Work 實作 |
| `internal/infrastructure/persistence/postgres/advisory_lock.go` | Distributed lock |
| `internal/infrastructure/messaging/kafka_publisher.go` | Kafka publisher |
| `internal/infrastructure/messaging/kafka_consumer.go` | Payment Kafka consumer |
| `internal/infrastructure/messaging/saga_consumer.go` | Saga Kafka consumer |
| `internal/infrastructure/observability/metrics.go` | Prometheus counter / histogram 定義(由所有 `*_metrics.go` 實作共用) |
| `internal/infrastructure/observability/worker_metrics.go` | `application.WorkerMetrics` 的 Prometheus 實作 |
| `internal/infrastructure/observability/booking_metrics.go` | `application.BookingMetrics` 的 Prometheus 實作 |
| `internal/infrastructure/observability/db_metrics.go` | `application.DBMetrics` 的 Prometheus 實作 |
| `internal/infrastructure/observability/queue_metrics.go` | `application.QueueMetrics` 的 Prometheus 實作 |
| `internal/infrastructure/config/config.go` | YAML config + 環境變數 override |
| `internal/infrastructure/payment/mock_gateway.go` | Mock 付款閘道 |

### 內部 logging(`internal/log/`)
| 檔案 | 用途 |
|------|------|
| `internal/log/log.go` | `Logger` 型別,包住 `*zap.Logger` 與 `AtomicLevel`。Ctx-aware emit 方法 `Debug/Info/Warn/Error/Fatal(ctx, msg, fields...)` 會透過 `enrichFields` 自動注入 `correlation_id` 與 OTEL `trace_id`/`span_id`。同時保留 `L()`(熱迴圈使用的原始 zap)、`S()`(sugar)、`With()`、`Level()`、`Sync()`。內部使用獨立的 `zCtxSkip` core 加上 `AddCallerSkip(2)`,確保 caller 指向使用者程式碼而非 wrapper |
| `internal/log/options.go` | `Options` 結構 + `fillDefaults`(encoder、output、sampling)。避免 log 套件反過來相依 `internal/config` |
| `internal/log/level.go` | `Level` 型別別名 + `ParseLevel(string) (Level, error)` — 打錯直接讓 app 啟動失敗,不靜默 fallback |
| `internal/log/context.go` | `NewContext` / `FromContext` — 慣例的 context 夾帶 logger 入口(照 klog/slog 命名)。`FromContext` 未設時回傳 `Nop`,不接全域 |
| `internal/log/nop.go` | `NewNop()` 靜默 logger,供測試與尚未接好的背景路徑使用 |
| `internal/log/handler.go` | `LevelHandler()` — GET/POST `/admin/loglevel` 動態調 level;掛在 pprof listener 上 |
| `internal/log/tag/tag.go` | 強型別 `zap.Field` 建構子(`tag.OrderID`、`tag.Error` 等)— hot path 的 key 不怕打錯 |
| `internal/log/field.go` | `Field` 型別別名 + 重新匯出的 zap 建構子(`log.String`、`log.Int`、`log.Int64`、`log.ByteString`、`log.Err`、`log.NamedError`),供 one-off inline key 使用,應用程式碼不需要再直接 import `go.uber.org/zap`。zap 完全封裝在 `internal/log/` 套件內部 |
| `internal/bootstrap/logmodule.go` | fx 綁定:讀 `cfg.App.LogLevel` → `log.ParseLevel` → `log.New`。放這裡(而非 `internal/log/` 內)讓 log 套件與 config 解耦 |

### 設定與部署
| 檔案 | 用途 |
|------|------|
| `config/config.yml` | 應用程式設定 |
| `.env.example` | 必要環境變數的範本(Postgres / Redis / Kafka / Grafana / OTel)。`.env` 本身已被 gitignore |
| `docker-compose.yml` | 完整環境編排(10 個服務);所有 secrets 都透過 `${VAR}` 取代,缺值時 fail fast |
| `Dockerfile` | 多階段建置(`golang:1.24-alpine` → `alpine:3.20`),runner stage 以 non-root uid 10001 執行 |
| `deploy/postgres/migrations/` | 7 個 SQL migration 檔 |
| `deploy/redis/redis.conf` | Redis AOF 持久化設定(密碼透過 docker-compose 的 `--requirepass ${REDIS_PASSWORD}` 帶入,不寫死在 conf 裡) |
| `deploy/nginx/nginx.conf` | 限流 + reverse proxy;帶有界 proxy timeouts 與 upstream keepalive |
| `deploy/prometheus/prometheus.yml` | 抓取設定(15 秒間隔) |
| `deploy/prometheus/alerts.yml` | 告警規則(`HighErrorRate`、`HighLatency`、`InventorySoldOut`、`KafkaConsumerStuck`) |
| `deploy/grafana/provisioning/` | 預設資料來源 + 儀表板(使用 `timeseries` 面板,`disableDeletion: true`) |

### Benchmark 與 Profiling 腳本
| 檔案 | 用途 |
|------|------|
| `scripts/k6_comparison.js` | k6 壓測情境(50 萬票池、500 VUs、60 秒 constant-vus),兩套 benchmark harness 都用它 |
| `scripts/benchmark_compare.sh` | 連跑兩輪的 A/B benchmark,產出符合歷史格式的比較報告 |
| `scripts/benchmark_gc.sh` | Phase 14:同時跑 k6 + gc_metrics + pprof,產出帶 GC Runtime Metrics 表格的報告 |
| `scripts/gc_metrics.sh` | 每 5 秒 poll `/metrics`,把 `go_gc_duration_seconds`、`go_memstats_*`、`go_goroutines` 寫成 CSV |
| `scripts/pprof_capture.sh` | 壓測途中從 `:6060` 抓 heap + allocs(30 秒取樣)+ goroutine profile |

### 文件
| 檔案 | 用途 |
|------|------|
| `docs/scaling_roadmap.md` | Stage 1-4 演進計劃 |
| `docs/architecture/current_monolith.md` | Phase 7.7 Mermaid 圖 |
| `docs/architecture/future_robust_monolith.md` | Phases 8-11 目標架構 |
| `docs/adr/0001_async_queue_selection.md` | Redis Streams vs Kafka 決策紀錄 |
| `docs/reviews/phase2_review.md` | Redis 整合 code review |
| `docs/reviews/ACTION_LIST.md` | Phase 13 的彙整 remediation 清單(66 項 findings,依嚴重度排序,連結回原始 review PR) |
| `docs/reviews/SMOKE_TEST_PLAN.md` | 12 個可重複執行的 smoke test 章節,涵蓋 CRITICAL / HIGH 修復(metric 預初始化、舊 route 移除、config validation、DLQ 路徑等) |
| `docs/benchmarks/` | 帶時間戳的效能報告;Phase 14 的 baseline 與 GC 測試存放於 `*_gc_*` 與 `*_compare_c500` 前綴 |
