# Booking Monitor 系統

> English version: [README.md](README.md)

一個針對搶票(Flash Sale)情境(10 萬以上併發使用者)設計的高並發票務訂票模擬系統。從單純的 DB 存取逐步演進到包含快取、非同步佇列、以及 Saga 模式的架構。

## 系統架構

```
Client -> Nginx (限流) -> Gin API -> Redis Lua (原子扣減)
  -> Redis Stream -> Worker -> PostgreSQL 交易 [Order + Outbox]
    -> OutboxRelay -> Kafka (order.created)
      -> PaymentWorker -> 成功: confirmed | 失敗: order.failed
        -> SagaCompensator -> 回滾 DB 與 Redis 庫存
```

**設計風格**:Domain-Driven Design + Clean Architecture(Modular Monolith)

```
cmd/booking-cli/          # CLI 進入點(server, stress, payment 指令)
internal/
  domain/                 # Entities(Event, Order)與 repository 介面
  application/            # Services: BookingService, WorkerService, OutboxRelay, SagaCompensator
  infrastructure/
    api/                  # Gin HTTP handlers + middleware
    cache/                # Redis: inventory、streams、idempotency、Lua scripts
    persistence/postgres/ # Repositories、UoW、advisory lock
    messaging/            # Kafka publisher + consumers
    observability/        # Prometheus metrics、OTEL tracing
    payment/              # Mock 付款閘道
    config/               # YAML config + 環境變數 override
pkg/logger/               # 結構化日誌(Zap)
deploy/                   # Postgres migrations、Redis、Nginx、Prometheus、Grafana 設定
```

## 特色

- **雙層庫存**:Redis(熱路徑,次毫秒等級) + PostgreSQL(事實來源)
- **非同步處理**:Redis Streams consumer group,含 PEL 恢復機制
- **Transactional Outbox**:訂單與事件同一筆交易,再由 OutboxRelay 發布至 Kafka
- **Saga 補償**:冪等地回滾付款失敗(DB + Redis)
- **三層冪等性**:API(header)、Worker(DB 索引)、Saga(Redis SETNX)
- **限流**:Nginx(100 req/s/IP,burst 200)
- **領導者選舉**:以 PostgreSQL advisory lock 確保只有 1 個 OutboxRelay 實例在跑
- **完整可觀測性**:Prometheus metrics、Grafana dashboards、Jaeger tracing、Zap logging
- **Correlation ID**:端到端跨元件的請求追蹤

## 先決條件

- Go 1.24+
- Docker 與 Docker Compose
- `golangci-lint`(執行 lint 用)
- `golang-migrate`(執行 DB migration 用)
- K6(選用,用於壓測)

## 快速上手

1. **啟動基礎設施**
   ```bash
   docker-compose up -d
   ```

2. **執行 migrations**
   ```bash
   make migrate-up
   ```

3. **建置並啟動 API server**
   ```bash
   make run-server
   ```
   Server 會監聽 8080 port,metrics 在 `/metrics`。

4. **啟動 Payment Worker**(另一個 terminal)
   ```bash
   ./bin/booking-cli payment
   ```

5. **重置狀態**(測試用)
   ```bash
   make reset-db
   ```

## API 端點

| Method | Path | 說明 |
|--------|------|------|
| POST | `/api/v1/book` | 訂票 `{ user_id, event_id, quantity }` |
| GET | `/api/v1/history` | 訂單歷史 `?page=1&size=10&status=confirmed` |
| POST | `/api/v1/events` | 建立活動 `{ name, total_tickets }` |
| GET | `/api/v1/events/:id` | 查看活動 |
| GET | `/metrics` | Prometheus 指標 |
| POST | `/book` | 舊版 route(Phase 0 保留) |

**冪等性**:POST /book 可帶 `Idempotency-Key: <uuid>` header,以達到 at-most-once 語意。

## 開發指令

```bash
make build              # 以 race detection 建置
make test               # 跑測試(含 race detection)
make lint               # 執行 golangci-lint
make mocks              # 重新產生 mock 檔
make run-stress C=100 N=500   # Go 壓測
make stress-k6 VUS=500 DURATION=30s  # K6 壓測
make benchmark VUS=1000 DURATION=60s  # 完整 benchmark 並記錄報告
make reset-db           # 重設 DB + Redis
make migrate-up         # 執行 migrations
make migrate-down       # 回退最近一個 migration
make docker-restart     # 重新 build 並重啟 app container
make curl-history PAGE=1 SIZE=5 STATUS=confirmed  # 查詢訂單歷史
```

## 可觀測性

| 工具 | URL | 用途 |
|------|-----|------|
| Prometheus | `http://localhost:9090` | 指標抓取 |
| Grafana | `http://localhost:3000`(admin/admin) | 6 格儀表板(RPS、延遲、轉換率、公平性、飽和度) |
| Jaeger | `http://localhost:16686` | 分散式追蹤 |

**關鍵指標**:`bookings_total`, `http_request_duration_seconds`, `worker_orders_total`, `inventory_conflicts_total`, `page_views_total`

## Docker 服務

| 服務 | Port | 說明 |
|------|------|------|
| app | 8080 | Booking API server |
| nginx | 80 | Reverse proxy + 限流 |
| payment_worker | - | Kafka 付款 consumer |
| postgres | 5433 | PostgreSQL 資料庫 |
| redis | 6379 | 快取 + streams |
| kafka | 9092 | 事件串流 |
| zookeeper | 2181 | Kafka 協調服務 |
| prometheus | 9090 | 指標收集 |
| grafana | 3000 | 儀表板 |
| jaeger | 16686/4317 | 分散式追蹤 |

## 設定

透過 YAML(`config/config.yml`)設定,並允許以環境變數 override:

| 設定 | 預設值 | 環境變數 |
|------|--------|----------|
| Server port | 8080 | PORT |
| Redis address | localhost:6379 | REDIS_ADDR |
| Kafka brokers | localhost:9092 | KAFKA_BROKERS |
| DB URL | postgres://user:password@localhost:5433/booking | DATABASE_URL |
| Log level | info | LOG_LEVEL |

## 效能

| 架構設定 | RPS | P99 延遲 |
|---------|-----|----------|
| 純 Postgres | ~4,000 | ~500ms |
| + Redis 熱庫存 | ~11,000 | ~50ms |
| + Kafka outbox | ~9,000 | ~100ms |
| + Saga 補償 | ~8,500 | ~120ms |

## 文件

- [Project Specification](docs/PROJECT_SPEC.zh-TW.md) — 完整系統規格(中文)
- [Project Specification (EN)](docs/PROJECT_SPEC.md) — 完整系統規格(英文)
- [Scaling Roadmap](docs/scaling_roadmap.md) — Stage 1-4 演進計劃
- [Architecture (Current)](docs/architecture/current_monolith.md) — Phase 7.7 圖
- [Architecture (Future)](docs/architecture/future_robust_monolith.md) — 目標架構
- [ADR-001: Queue Selection](docs/adr/0001_async_queue_selection.md) — Redis Streams vs Kafka 決策
- [Phase 2 Review](docs/reviews/phase2_review.md) — Redis 整合 review
- [Benchmarks](docs/benchmarks/) — 15 份效能報告
