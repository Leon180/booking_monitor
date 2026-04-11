# Booking Monitor - 專案規格書

> English version: [PROJECT_SPEC.md](PROJECT_SPEC.md)

## 1. 專案概述

一個用來模擬「搶票 / Flash Sale」情境(10 萬以上併發使用者)的高並發票務訂票系統。以 Go 撰寫,遵循 DDD 與 Clean Architecture。

**核心目標**:以多層防線防止超賣,同時將吞吐量最大化。

**開發時程**:2026-02-14 至 2026-02-24(10 天,15 個 commits,12 個 phases)。2026-04-11 進行多 agent 程式碼審查,總共整理出 66 項 findings,並透過 PR #8(CRITICAL)、#9(HIGH)、#10(MEDIUM/LOW/NIT)完成修復 — 詳見第 8 節。

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

| 步驟 | 元件 | 儲存層 | 採用模式 |
|------|------|--------|----------|
| 1 | API 接收訂票請求 | Redis | Lua 原子扣減 |
| 2 | Worker 處理訂單 | PostgreSQL | UoW(Order + Outbox 同一交易) |
| 3 | OutboxRelay 發布事件 | Kafka | Transactional Outbox |
| 4 | 付款處理 | PostgreSQL | 狀態更新 |
| 5 | 失敗時補償 | PostgreSQL + Redis | Saga 模式 |

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
| PaymentService | 處理付款事件(遇到無效輸入回傳 `ErrInvalidPaymentEvent`,讓 consumer 能 dead-letter) | 領域層服務 |
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
{ "user_id": 123, "event_id": 1, "quantity": 1 }
// Headers: Idempotency-Key: <uuid>(選填,<= 128 字元)

// 200 OK
{ "message": "booking successful" }
// 409 Conflict
{ "error": "sold out" }
// 409 Conflict
{ "error": "user already bought ticket" }
// 500 Internal Server Error(已 sanitize)
{ "error": "internal server error" }
```

錯誤回應都會通過 `api/errors.go :: mapError`:透過 `errors.Is` 比對 sentinel 錯誤後,回傳一個安全的公開訊息。原始的 DB / driver 錯誤只會記錄在伺服器端(帶 correlation ID),**絕不**回給 client。

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

`deploy/prometheus/alerts.yml` 裡的 `InventorySoldOut` 告警改成 `increase(bookings_total{status="sold_out"}[5m]) > 0` — 之前的 `booking_sold_out_total` 表達式指向一個程式碼裡不存在的指標,所以告警永遠不會觸發。

### 分散式追蹤(OpenTelemetry + Jaeger)
- Decorator 模式:`BookingServiceTracingDecorator`, `WorkerServiceMetricsDecorator`, `OutboxRelayTracingDecorator`
- `OutboxRelayTracingDecorator` 現在會在批次失敗時呼叫 `span.RecordError` + `span.SetStatus(codes.Error)` — 舊版 span 永遠是 OK
- `api/handler_tracing.go` 用共用的 `recordHTTPResult(span, status)` helper,對**所有** status >= 400 都會 set `span.status = Error`(不只是 5xx),這樣 4xx client 錯誤也能在 Jaeger 搜尋中浮現
- GRPC exporter 連至 Jaeger(port 4317)
- **採樣器可設定**:透過 `OTEL_TRACES_SAMPLER_RATIO`:空/1 → AlwaysSample(預設)、0 → NeverSample、0 < r < 1 → TraceIDRatioBased(r)。若值無法解析,會 log warning 並 fallback 到 AlwaysSample(絕不靜默關閉 tracing)
- `initTracer` 現在會**fail fast**(回傳錯誤給 fx.Invoke),不再讓 `resource.New` 或 `otlptracegrpc.New` 的錯誤落到下一步,導致 nil `traceExporter` 在送第一個 span 時 crash

### 日誌(Zap)
- Structured JSON 輸出至 stdout
- Middleware 注入 Correlation ID
- 依元件劃分的 logger

### 儀表板(Grafana)
預先配置的 6 格儀表板: RPS、Latency Quantiles、Conversion Rate、IP Fairness、Saturation

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
| 13 | 4/11 | PRs #7–#10 | **多 agent review 與 remediation**:在 6 個 review 面向(domain/app、persistence、concurrency/cache、messaging/saga、api/payment、observability/deploy)共彙整出 66 項 findings。所有 6 個 CRITICAL 與 13 個 HIGH 在 [`fix/review-critical` (#8)](https://github.com/Leon180/booking_monitor/pull/8) 與 [`fix/review-high` (#9)](https://github.com/Leon180/booking_monitor/pull/9) 修復;17 MEDIUM / 14 LOW / 6 NIT 在 [`fix/review-backlog` (#10)](https://github.com/Leon180/booking_monitor/pull/10) 修復。彙整後的 backlog 存放在 [`docs/reviews/ACTION_LIST.md`](reviews/ACTION_LIST.md)。使用者面向的改動請看下方的 **Remediation 重點** 區塊。 |

### Remediation 重點(Phase 13)

- **Kafka DLQ 全鏈路**:新增 topics `order.created.dlq` / `order.failed.dlq`、新增指標 `dlq_messages_total` / `saga_poison_messages_total`、saga 重試計數改為 Redis 持久化、新增 `ErrInvalidPaymentEvent` sentinel。從此不會再有靜默 drop 的訊息。
- **API 安全**:`r.Run()` 改為顯式建立的 `http.Server{}`,真正套用 `cfg.Server.ReadTimeout`/`WriteTimeout`;`api/errors.go :: mapError` 對每一個錯誤回應做 sanitize,DB / driver 錯誤絕不會外洩;舊版 `POST /book` 已移除。
- **Secrets 搬到 `.env`**:所有明文密碼(`postgres`、`grafana`、`redis`)都改從 gitignore 的 `.env` 透過 `${VAR}` 取代,並提供追蹤中的 `.env.example`;docker-compose 在缺少必要值時會 fail fast。
- **`Config.Validate()`** 會拒絕缺少的 `DATABASE_URL`,並在 `APP_ENV=production` 時禁止 `REDIS_ADDR` / `KAFKA_BROKERS` 使用 localhost 預設。
- **Deploy 強化**:六個原本未 pin 的 image 全部 pin 住(`golang:1.24-alpine`、`alpine:3.20`、`nginx:1.27-alpine`、`prom/prometheus:v2.54.1`、`grafana/grafana:11.2.2`、`jaegertracing/all-in-one:1.60`);Dockerfile runner stage 改以 non-root `uid:10001` 執行;Redis 加上 `--requirepass`。
- **可觀測性**:OTel 採樣器可透過 `OTEL_TRACES_SAMPLER_RATIO` 設定;`recordHTTPResult` helper 會把 4xx 也標成 span error;`InventorySoldOut` 告警改用真實存在的 `bookings_total{status="sold_out"}` 指標。
- **Persistence**:新增部分索引 `events_outbox_pending_idx`(migration 000007);pool 設定移到 ping 迴圈之前並加上 `ConnMaxLifetime`;`GetByID` 拆成一般版 + `GetByIDForUpdate`;19 處 repository 的錯誤都改用 `%w` wrap。

---

## 9. 效能基準

| 設定 | RPS | P99 延遲 | 瓶頸 |
|------|-----|----------|------|
| Stage 1:純 Postgres | ~4,000 | ~500ms | DB CPU(212%) |
| Stage 2:Redis 熱庫存 | ~11,000 | ~50ms | 記憶體/網路 |
| Stage 2 + Kafka outbox | ~9,000 | ~100ms | Kafka 吞吐量 |
| 完整系統(含 saga) | ~8,500 | ~120ms | Saga overhead |

基準測試報告在 `docs/benchmarks/`(共 15 個時間戳目錄)。

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
| `cmd/booking-cli/main.go` | CLI 進入點:`server`, `stress`, `payment` 指令 |
| `cmd/verify-redis/main.go` | Redis 驗證工具 |

### Domain
| 檔案 | 用途 |
|------|------|
| `internal/domain/event.go` | Event entity + OutboxEvent + Kafka 事件型別 |
| `internal/domain/order.go` | Order entity + 狀態常數 |
| `internal/domain/repositories.go` | Repository 介面 |
| `internal/domain/inventory.go` | InventoryRepository 介面 |
| `internal/domain/queue.go` | OrderQueue 介面 |
| `internal/domain/messaging.go` | EventPublisher 介面 |
| `internal/domain/payment.go` | PaymentGateway + PaymentService 介面 |
| `internal/domain/lock.go` | DistributedLock 介面 |
| `internal/domain/idempotency.go` | IdempotencyRepository 介面 |
| `internal/domain/worker_metrics.go` | WorkerMetrics 介面 |
| `internal/domain/uow.go` | UnitOfWork 介面 |

### Application Services
| 檔案 | 用途 |
|------|------|
| `internal/application/booking_service.go` | 訂票主邏輯(Redis 扣減) |
| `internal/application/worker_service.go` | 從 stream 讀取訂單的背景處理 |
| `internal/application/outbox_relay.go` | Outbox 發布至 Kafka |
| `internal/application/saga_compensator.go` | 付款失敗補償 |
| `internal/application/payment/service.go` | 付款處理邏輯 |

### Infrastructure
| 檔案 | 用途 |
|------|------|
| `internal/infrastructure/api/handler.go` | HTTP handlers + 路由註冊 |
| `internal/infrastructure/api/errors.go` | `mapError(err) (status, publicMsg)` helper — 負責把錯誤 sanitize 成公開訊息 |
| `internal/infrastructure/api/handler_tracing.go` | Tracing decorator + `recordHTTPResult` span helper |
| `internal/infrastructure/api/middleware/` | 冪等性、correlation ID、metrics、tracing |
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
| `internal/infrastructure/observability/metrics.go` | Prometheus metrics 設定 |
| `internal/infrastructure/config/config.go` | YAML config + 環境變數 override |
| `internal/infrastructure/payment/mock_gateway.go` | Mock 付款閘道 |

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
| `deploy/prometheus/alerts.yml` | 告警規則(`HighErrorRate`、`HighLatency`、`InventorySoldOut`) |
| `deploy/grafana/provisioning/` | 預設資料來源 + 儀表板(使用 `timeseries` 面板,`disableDeletion: true`) |

### 文件
| 檔案 | 用途 |
|------|------|
| `docs/scaling_roadmap.md` | Stage 1-4 演進計劃 |
| `docs/architecture/current_monolith.md` | Phase 7.7 Mermaid 圖 |
| `docs/architecture/future_robust_monolith.md` | Phases 8-11 目標架構 |
| `docs/adr/0001_async_queue_selection.md` | Redis Streams vs Kafka 決策紀錄 |
| `docs/reviews/phase2_review.md` | Redis 整合 code review |
| `docs/reviews/ACTION_LIST.md` | Phase 13 的彙整 remediation 清單(66 項 findings,依嚴重度排序,連結回原始 review PR) |
| `docs/benchmarks/` | 15 份帶時間戳的效能報告 |
