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
cmd/booking-cli/          # CLI 進入點:server / stress / payment / recon / saga-watchdog 子指令
internal/
  domain/                 # Entities(Event、Order、OutboxEvent)、value type(StuckCharging、StuckFailed)、repository 介面
  application/            # 跨子套件 fx module + UnitOfWork 介面 + wire-format 事件 DTO(order_events.go)
    booking/              # POST /book 熱路徑:BookTicket 驗證 + Redis Lua 扣減
    worker/               # 訂單 stream consumer + queue 策略 + 單筆訊息 processor
    outbox/               # Outbox relay 輪詢 + Kafka 發布(transactional outbox 實作)
    event/                # 活動建立 + Redis 熱庫存初始化
    payment/              # Kafka order.created consumer + gateway 編排 + saga 觸發
    recon/                # Reconciler(A4)— 掃描卡在 `charging` 的訂單,呼叫 gateway 收尾
    saga/                 # Compensator + Watchdog(A5)— order.failed consumer + DB 端 sweep
  infrastructure/
    api/
      booking/            # POST /book、GET /orders、GET /history、POST /events、GET /events/:id
      ops/                # /livez、/readyz、/metrics
      middleware/         # Idempotency(N4)、correlation_id、metrics
      dto/                # Wire-format request/response 結構
    cache/                # Redis:inventory、streams、idempotency、Lua scripts(deduct.lua、revert.lua)
    persistence/postgres/ # Repositories、UoW、advisory lock、row mapper
    messaging/            # Kafka publisher + consumers
    observability/        # Prometheus metrics、OTEL tracing、DB pool collector
    payment/              # Mock 付款閘道(成功率可設定)
    config/               # YAML config + 環境變數 override(cleanenv)
  log/                    # 結構化日誌(Zap)— context 傳遞、typed tag、執行期 level
  bootstrap/              # logger + tracer + DI 基礎元件的 fx 綁定
deploy/                   # Postgres migrations(11 個)、Redis Lua、Nginx、Prometheus alert、Grafana dashboard
```

## 特色

- **雙層庫存**:Redis(熱路徑,次毫秒等級) + PostgreSQL(事實來源)
- **非同步處理**:Redis Streams consumer group,含 PEL 恢復機制
- **Transactional Outbox**:訂單與事件同一筆交易,再由 OutboxRelay 發布至 Kafka
- **Saga 補償**:冪等地回滾付款失敗(DB + Redis)
- **四層冪等性**:API(`Idempotency-Key` header + N4 fingerprint 驗證)、Worker(DB UNIQUE 索引)、Saga(Redis SETNX)、付款閘道(mock gateway 實作 idempotent `Charge`)
- **限流**:Nginx(100 req/s/IP,burst 200)
- **領導者選舉**:以 PostgreSQL advisory lock 確保只有 1 個 OutboxRelay 實例在跑
- **完整可觀測性**:Prometheus metrics、Grafana dashboards、Jaeger tracing、Zap logging
- **Correlation ID**:端到端跨元件的請求追蹤

## 先決條件

- Go 1.25+(透過 `go.mod` 的 `toolchain go1.25.9` 釘版)
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
| POST | `/api/v1/book` | 提交訂票。回 **202 Accepted** + 一個 `order_id` 用來追蹤訂單(詳見下方「**訂票流程**」)。 |
| GET | `/api/v1/orders/:id` | 用 `order_id` 查單筆訂單的最新狀態。在 `POST /book` 之後的短暫視窗裡會回 404(詳見「**訂票流程**」)。 |
| GET | `/api/v1/history` | 訂單歷史 `?page=1&size=10&status=confirmed` |
| POST | `/api/v1/events` | 建立活動 `{ name, total_tickets }` |
| GET | `/api/v1/events/:id` | **Stub** — 回 `{"message": "View event", "event_id": ...}` 並遞增 `page_views_total` 給轉換率追蹤。**不會**載入活動詳情(延後到 Phase 3 demo 才實作)。 |
| GET | `/metrics` | Prometheus 指標 |
| GET | `/livez` | Liveness 探針 — process 還活著就一律回 200(不依賴下游) |
| GET | `/readyz` | Readiness 探針 — PG + Redis + Kafka 都在 1s 內回應才回 200,否則回 503 並附逐 dep 的 JSON |

### 訂票流程

`POST /api/v1/book` 在設計上**就是非同步的**。回 202(不是 200)是誠實的:在這個回應的當下,只完成了 Redis 端的庫存扣減 — 訂單還沒寫進資料庫、付款還沒嘗試、訂票其實還沒真正成功。Client 拿到 `order_id` 之後,要自己輪詢最終狀態。

```
1. Client → POST /api/v1/book { user_id, event_id, quantity }
2. Server → 202 Accepted {
       order_id: "019dd493-47ae-79b1-b954-8e0f14a6a482",
       status:   "processing",
       message:  "booking accepted, awaiting confirmation",
       links:    { self: "/api/v1/orders/019dd493-..." }
   }

   此時:
   - Redis 庫存:已扣減(這是 load-shed gate)
   - DB orders 列:還沒寫入(worker ~ms 後才會寫)
   - 付款:還沒嘗試
   - 結果:還不知道

3. Client → GET /api/v1/orders/<order_id>  (帶 backoff 輪詢:100ms → 250ms → 500ms ...)

   可能的回應:
   - 404  → worker 還沒寫進 DB。重試即可。
   - 200  → { id, user_id, event_id, quantity, status, created_at }
            其中 `status` 為:
              "pending"     — DB 已寫入,等待付款
              "charging"    — 付款進行中
              "confirmed"   — 付款成功 + 訂票完成    ✓ 終態(成功)
              "failed"      — 付款失敗,saga 即將回滾
              "compensated" — saga 已回滾庫存         ✓ 終態(失敗)
```

**為什麼不直接同步回終態?** Redis-first 是 **load-shed gate** — flash-sale 流量下,售完的請求會在 Redis 層就被擋掉,根本不會碰到資料庫。如果 `POST /book` 同步等到終態,每一個請求都會佔用整個付款 round-trip(數秒)的連線,整體吞吐就被最慢的依賴卡住。Flash-sale 系統的業界標準做法(Tmall、KKTIX、Ticketmaster 都這樣)。

**冪等性**:`POST /api/v1/book` 可以帶 `Idempotency-Key: <ASCII 可印字元、≤128 字元>` header,達到 at-most-once 語意。重送時會回原本的 202 回應(同樣的 `order_id`),並加上 `X-Idempotency-Replayed: true` header。Cache TTL:24h。**Stripe 風格的 fingerprint 檢查(N4)**:同 key 但 body **不同** 時會回 **409 Conflict** 而不是重播 — 避免 client 弄錯(同一個 key 用在不同語意的請求上)後靜默拿到錯誤的回應。4xx 驗證錯誤 **不會** 被快取,所以一次手誤打錯 body 不會把 key 燒掉 24h。完整契約表見 [docs/PROJECT_SPEC.zh-TW.md §5](docs/PROJECT_SPEC.zh-TW.md)。

**404 視窗的實際情況**:健康的 worker 通常 < 1 秒。如果持續 404 表示 worker 已經塞車 — 可以從 `redis_stream_length{stream="orders:stream"}` 指標或 `OrdersStreamBacklog*` 告警觀察(見 [docs/monitoring.zh-TW.md](docs/monitoring.zh-TW.md))。

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
| Grafana | `http://localhost:3000`(admin/admin) | 6 格儀表板(RPS、p99/p95/p50 延遲、轉換率、goroutines、記憶體) |
| Jaeger | `http://localhost:16686` | 分散式追蹤 |

**關鍵指標**:`bookings_total`, `http_request_duration_seconds`, `worker_orders_total`, `inventory_conflicts_total`, `page_views_total`

## Docker 服務

| 服務 | Port | 說明 |
|------|------|------|
| app | 8080 | Booking API server |
| nginx | 80 | Reverse proxy + 限流 |
| payment_worker | - | Kafka 付款 consumer(`booking-cli payment`) |
| recon | - | 卡單收尾的 Reconciler 子指令(`booking-cli recon`) |
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

架構演進的歷史快照(早期 phase — 用來呈現演進敘事,不是當前數字):

| 架構設定 | RPS | P99 延遲 |
|---------|-----|----------|
| 純 Postgres | ~4,000 | ~500ms |
| + Redis 熱庫存 | ~11,000 | ~50ms |
| + Kafka outbox | ~9,000 | ~100ms |
| + Saga 補償 | ~8,500 | ~120ms |

**當前 baseline**(PR #45 之後、GC 已調過、c=500 VUs、60s、500k 票池、直連 `app:8080`):**~54,000 RPS / p95 ~12.6ms**([20260428_225152_compare_c500_a4_charging_intent](docs/benchmarks/20260428_225152_compare_c500_a4_charging_intent/comparison.md))。上方的早期數字是 PR #14/#15 GC 調整(`GOGC=400`、`GOMEMLIMIT=256MiB`、Lua args 的 sync.Pool)落地之前的歷史紀錄。所有逐次比對報告都在 [docs/benchmarks/](docs/benchmarks/);apples-to-apples 標準設定見 [CLAUDE.md「Benchmark 慣例」](.claude/CLAUDE.md)。

## 文件

- [Project Specification](docs/PROJECT_SPEC.zh-TW.md) — 完整系統規格(中文)
- [Project Specification (EN)](docs/PROJECT_SPEC.md) — 完整系統規格(英文)
- [Post-Phase-2 Roadmap](docs/post_phase2_roadmap.md) — **目前的 sprint plan + Pattern A demo 順序**(「下一步要做什麼」的權威來源)
- [Project Review Checkpoints](docs/checkpoints/) — 各個 phase 邊界的全專案審計報告
- [Scaling Roadmap](docs/scaling_roadmap.md) — 歷史性的 Stage 1-4 架構演進敘事
- [Architecture (Current)](docs/architecture/current_monolith.md) — 目前架構的 Mermaid 圖
- [Architecture (Future)](docs/architecture/future_robust_monolith.md) — 目標架構
- [ADR-001: Queue Selection](docs/adr/0001_async_queue_selection.md) — Redis Streams vs Kafka 決策
- [Phase 2 Review](docs/reviews/phase2_review.md) — 早期 phase 的 Redis 整合 review
- [Benchmarks](docs/benchmarks/) — 歷次效能比對報告
