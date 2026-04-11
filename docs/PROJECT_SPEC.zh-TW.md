# Booking Monitor - 專案規格書

> English version: [PROJECT_SPEC.md](PROJECT_SPEC.md)

## 1. 專案概述

一個用來模擬「搶票 / Flash Sale」情境(10 萬以上併發使用者)的高並發票務訂票系統。以 Go 撰寫,遵循 DDD 與 Clean Architecture。

**核心目標**:以多層防線防止超賣,同時將吞吐量最大化。

**開發時程**:2026-02-14 至 2026-02-24(10 天,15 個 commits,12 個 phases)

---

## 2. 系統架構

```
Client --> Nginx (限流: 100 req/s/IP, burst 200)
  --> Gin API (冪等性檢查, Correlation ID, metrics)
    --> BookingService
      --> Redis Lua Script (原子 DECRBY + XADD 至 stream)
        --> Redis Stream (orders:stream)
          --> WorkerService (consumer group, PEL 恢復)
            --> PostgreSQL 交易 [Order + OutboxEvent]
              --> OutboxRelay (advisory lock 領導者選舉)
                --> Kafka (order.created)
                  --> PaymentWorker
                    --> 成功: UPDATE status='confirmed'
                    --> 失敗: Outbox -> Kafka (order.failed)
                      --> SagaCompensator
                        --> DB: IncrementTicket + status='compensated'
                        --> Redis: SETNX + INCRBY (冪等回滾)
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
方法: Deduct(quantity) — 在扣減前驗證容量
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
| EventRepository | Event CRUD + DecrementTicket/IncrementTicket | PostgreSQL |
| OrderRepository | Order CRUD + 狀態更新 | PostgreSQL |
| OutboxRepository | Outbox CRUD + ListPending/MarkProcessed | PostgreSQL |
| InventoryRepository | 熱庫存扣減/回滾 | Redis(Lua scripts) |
| OrderQueue | 非同步訂單串流(Enqueue/Dequeue/Ack) | Redis Streams |
| IdempotencyRepository | 請求去重(24 小時 TTL) | Redis |
| EventPublisher | 發布領域事件 | Kafka |
| PaymentGateway | 扣款 | Mock(可設定成功率) |
| DistributedLock | 領導者選舉 | PostgreSQL advisory locks |
| UnitOfWork | 交易管理 | PostgreSQL |

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

### Migration 歷史(`deploy/postgres/migrations/` 內共 6 個檔案)

| # | 內容 |
|---|------|
| 000001 | 建立 `events` 資料表 + 初始化 "Jay Chou Concert"(100 張票) |
| 000002 | 建立 `orders` 資料表 |
| 000003 | 建立 `events_outbox` 資料表 |
| 000004 | 加入 `check_available_tickets_non_negative` + `UNIQUE(user_id, event_id)` |
| 000005 | 為 `events_outbox` 加上 `processed_at` 欄位 |
| 000006 | 以部分索引 `WHERE status != 'failed'` 取代 unique constraint — 允許使用者在付款失敗後重試購買 |

### Redis

| Key Pattern | 型別 | 用途 |
|-------------|------|------|
| `event:{id}:qty` | String (integer) | 熱庫存計數器 |
| `orders:stream` | Stream | 非同步訂單佇列 |
| `idempotency:{key}` | String | 請求去重(24 小時 TTL) |
| `saga:reverted:{order_id}` | String | 補償冪等性(7 天 TTL) |

### Kafka Topics

| Topic | Producer | Consumer Group | Consumer | Payload |
|-------|----------|----------------|----------|---------|
| `order.created` | OutboxRelay | `payment-service-group-test` | PaymentWorker | OrderCreatedEvent (id, user_id, event_id, quantity, amount) |
| `order.failed` | PaymentWorker(透過 outbox) | `booking-saga-group` | SagaCompensator | OrderFailedEvent (order_id, event_id, user_id, quantity, reason) |

---

## 5. API 文件

### POST /api/v1/book
訂票。
```json
// Request
{ "user_id": 123, "event_id": 1, "quantity": 1 }
// Headers: Idempotency-Key: <uuid>(選填)

// 200 OK
{ "message": "booking successful" }
// 409 Conflict
{ "error": "sold out" }
// 409 Conflict
{ "error": "user already bought ticket" }
```

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

### POST /book(legacy)
未加版本號的舊版訂票端點,Phase 0 起保留至今。

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

**revert.lua** — 冪等補償
```
1. SETNX saga:reverted:{order_id}(避免重複回滾)
2. 若成功:EXPIRE 7d + INCRBY event:{id}:qty
3. 回傳 1(已回滾)或 0(之前已回滾)
```

### 6.2 Transactional Outbox

1. Worker 在同一 PostgreSQL 交易內寫入 `Order` + `OutboxEvent`
2. OutboxRelay(背景 goroutine)每 500ms 輪詢 `events_outbox WHERE processed_at IS NULL`
3. 將該批(上限 100 筆)發布至 Kafka
4. 每筆成功後標記為 processed
5. 透過 `pg_try_advisory_lock(1001)` 做領導者選舉,確保同時只有 1 個 instance 在發布

### 6.3 Saga 補償

1. PaymentWorker 消費 `order.created`,呼叫 PaymentGateway.Charge()
2. 失敗時:更新訂單狀態為 `failed`,並寫入 `order.failed` outbox 事件
3. SagaCompensator 消費 `order.failed`:
   - DB: IncrementTicket + 更新訂單狀態為 `compensated`(同一交易)
   - Redis: 以 SETNX 保護的 INCRBY 還原熱庫存
4. 最多 3 次重試,超過就跳過(避免 partition 被卡住)

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
| `http_requests_total` | Counter | 依 method/path/status 統計請求量 |
| `http_request_duration_seconds` | Histogram | 請求延遲(針對 p99 調整的 bucket) |
| `bookings_total` | Counter | 訂票結果(success, sold_out, duplicate, error) |
| `worker_orders_total` | Counter | Worker 處理結果 |
| `worker_processing_duration_seconds` | Histogram | Worker 延遲 |
| `inventory_conflicts_total` | Counter | Redis 通過但 DB 拒絕的超賣偵測 |
| `page_views_total` | Counter | 活動頁面瀏覽量(轉換漏斗) |

### 分散式追蹤(OpenTelemetry + Jaeger)
- Decorator 模式:`BookingServiceTracingDecorator`, `WorkerServiceMetricsDecorator`, `OutboxRelayTracingDecorator`
- GRPC exporter 連至 Jaeger(port 4317)
- 永遠採樣(always-sample)

### 日誌(Zap)
- Structured JSON 輸出至 stdout
- Middleware 注入 Correlation ID
- 依元件劃分的 logger

### 儀表板(Grafana)
預先配置的 6 格儀表板: RPS、Latency Quantiles、Conversion Rate、IP Fairness、Saturation

---

## 8. 開發階段(完整歷史)

| Phase | 日期 | Commit | 內容 |
|-------|------|--------|------|
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
- **DLQ Worker**:實作 dead letter 的重試政策(可設定 backoff 與最大次數)。目前 Worker 失敗 3 次後訊息會被搬到 `orders:dlq` 但從來沒有被重新處理。

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
| `docker-compose.yml` | 完整環境編排(10 個服務) |
| `Dockerfile` | 多階段建置(alpine, ~7MB) |
| `deploy/postgres/migrations/` | 6 個 SQL migration 檔 |
| `deploy/redis/redis.conf` | Redis AOF 持久化設定 |
| `deploy/nginx/nginx.conf` | 限流 + reverse proxy |
| `deploy/prometheus/prometheus.yml` | 抓取設定(5 秒間隔) |
| `deploy/prometheus/alerts.yml` | 告警規則 |
| `deploy/grafana/provisioning/` | 預設資料來源 + 儀表板 |

### 文件
| 檔案 | 用途 |
|------|------|
| `docs/scaling_roadmap.md` | Stage 1-4 演進計劃 |
| `docs/architecture/current_monolith.md` | Phase 7.7 Mermaid 圖 |
| `docs/architecture/future_robust_monolith.md` | Phases 8-11 目標架構 |
| `docs/adr/0001_async_queue_selection.md` | Redis Streams vs Kafka 決策紀錄 |
| `docs/reviews/phase2_review.md` | Redis 整合 code review |
| `docs/benchmarks/` | 15 份帶時間戳的效能報告 |
