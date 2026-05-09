# Booking Monitor 系統

> English version: [README.md](README.md)

一個針對搶票(Flash Sale)情境(10 萬以上併發使用者)設計的高並發票務訂票模擬系統。從單純的 DB 存取逐步演進到包含快取、非同步佇列、以及 Saga 模式的架構。

## 系統架構

目前的 production 樣貌(Stage 4 — 演進過程見下方[架構演進](#架構演進)):

```mermaid
flowchart LR
    Client((Client)) --> Nginx[Nginx<br/>限流]
    Nginx --> API[Gin API]
    API -->|Lua DECRBY + XADD| Redis[(Redis<br/>庫存 + stream)]
    Redis -.->|XReadGroup| Worker
    Worker -->|UoW: INSERT order| PG[(Postgres<br/>事實來源)]
    Client -->|POST /pay| API
    API -->|CreatePaymentIntent| Gateway[Payment<br/>Gateway]
    Gateway -.->|webhook<br/>signed| API
    API -->|UoW: MarkPaid OR<br/>MarkFailed + outbox| PG
    Sweeper[Expiry<br/>Sweeper] -->|UoW: MarkExpired<br/>+ outbox| PG
    PG -.->|advisory-lock<br/>leader relay| Kafka{{Kafka}}
    Kafka -.->|order.failed| Saga[Saga Consumer<br/>in-process]
    Saga --> Redis
    Saga --> PG
```

> 實線箭頭 = 同步熱路徑 · 虛線箭頭 = 非同步 / event-driven
> Cache-truth 契約見 [v0.4.0 release notes](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0):Redis 為 ephemeral、Postgres 為事實來源、漂移會被偵測並命名。

**設計風格**:Domain-Driven Design + Clean Architecture(Modular Monolith)

```
cmd/booking-cli/          # CLI 進入點:server / stress / recon / saga-watchdog / expiry-sweeper 子指令
internal/
  domain/                 # Entities(Event、Order、OutboxEvent)、value type(StuckCharging、StuckFailed)、repository 介面
  application/            # 跨子套件 fx module + UnitOfWork 介面 + wire-format 事件 DTO(order_events.go)
    booking/              # POST /book 熱路徑:BookTicket 驗證 + Redis Lua 扣減
    worker/               # 訂單 stream consumer + queue 策略 + 單筆訊息 processor
    outbox/               # Outbox relay 輪詢 + Kafka 發布(transactional outbox 實作)
    event/                # 活動建立 + Redis 熱庫存初始化
    payment/              # /pay(D4)handler + gateway adapter + D5 webhook
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
    payment/              # Mock 付款閘道(CreatePaymentIntent + GetStatus;成功/失敗由 D5 webhook 結果驅動,post-D7)
    config/               # YAML config + 環境變數 override(cleanenv)
  log/                    # 結構化日誌(Zap)— context 傳遞、typed tag、執行期 level
  bootstrap/              # logger + tracer + DI 基礎元件的 fx 綁定
deploy/                   # Postgres migrations(15 個)、Redis Lua、Nginx、Prometheus alert、Grafana dashboard
```

### 架構演進

這個架構不是一次到位的。每一層都是為了解決上一層 benchmark 量化出來的瓶頸 — 下面四個階段是 v0.1.0→v0.4.0 的演進故事(Stage 4 = v0.2.0–v0.4.0 的里程碑,**還沒被 v0.6.0 的 D7 收窄**前)。可以照著 [Releases page](https://github.com/Leon180/booking_monitor/releases) 一個 commit 一個 commit 走過去;當前(post-D7)的形狀則由上方 [top-level diagram](#系統架構) 呈現。

**Stage 1 — 同步 baseline。** API → Postgres `SELECT FOR UPDATE`。在 1k req/s 以下就因為 row-lock 爭用飽和。[v0.1.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.1.0) 的 C500 benchmark 紀錄了這個天花板。

```mermaid
flowchart LR
    C1((Client)) --> A1[API]
    A1 -->|SELECT FOR UPDATE<br/>+ INSERT| P1[(Postgres)]
```

**Stage 2 — Redis 熱路徑,DB 仍同步寫入。** 在 Redis 上做 Lua 原子扣減,row-lock 爭用消失,但同步 DB INSERT 變成新的瓶頸。

```mermaid
flowchart LR
    C2((Client)) --> A2[API]
    A2 -->|Lua DECRBY| R2[(Redis)]
    A2 -->|sync INSERT| P2[(Postgres)]
```

**Stage 3 — 透過 Redis Streams + worker pool 走非同步。** Lua deduct 把訊息送進 stream;worker pool 非同步消化。Redis 一回 ok,客戶端就拿到 HTTP 202。

```mermaid
flowchart LR
    C3((Client)) --> A3[API]
    A3 -->|Lua DECRBY + XADD| R3[(Redis)]
    R3 -.->|stream| W3[Worker]
    W3 -->|INSERT| P3[(Postgres)]
```

**Stage 4 — 完整 event-driven(v0.2.0–v0.4.0;pre-D7 歷史版本)。** Worker 在一個 UoW 裡同時寫 order 跟 outbox;advisory-lock 選出來的 leader relay 推訊息到 Kafka;payment service + saga compensator 是各自獨立的 Kafka consumer。就是 [v0.2.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.2.0) 釋出、再經過 [v0.3.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.3.0) + [v0.4.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.4.0) 強化的架構。[v0.6.0](https://github.com/Leon180/booking_monitor/releases/tag/v0.6.0) 的 D7 之後把它再收窄 — worker 只寫 order 一筆(不再 emit `order.created` outbox)、`payment_worker` binary 移除、saga consumer/compensator 改在 `app` 行程內執行。當前形狀請看上面的 [top-level diagram](#系統架構);下方的圖刻意保留作為 v0.2.0–v0.4.0 的里程碑參考(也是 D12 中 `cmd/booking-cli-stage4/` 要 benchmark 的版本)。

```mermaid
flowchart LR
    C4((Client)) --> A4[API]
    A4 -->|Lua DECRBY + XADD| R4[(Redis)]
    R4 -.->|stream| W4[Worker]
    W4 -->|UoW: order + outbox| P4[(Postgres)]
    P4 -.->|relay| K4{{Kafka}}
    K4 -.-> Pay4[Payment]
    K4 -.-> S4[Saga]
```

四個 stage 的 `cmd/booking-cli-stage{1,2,3,4}/` 比較 harness(D12;細節見 [`docs/d12/README.md`](docs/d12/README.md))已上線 — 同一份 `internal/` 程式碼、不同 fx 接線、平行跑 benchmark 並排比較。每次 apples-to-apples 比較報告都放在 [`docs/benchmarks/comparisons/`](docs/benchmarks/comparisons/),透過 `make bench-up` + `scripts/run_4stage_comparison.sh` + `scripts/generate_comparison_md.py` 產生。

### Pattern A 流程(已隨 v0.5.0 + v0.6.0 上線)

`POST /book` 已拆成預訂 + 顯式的 `POST /pay` + `POST /webhook/payment` — Stripe Checkout / KKTIX 形狀。下面的流程在 Docker stack 上端到端可跑。

```mermaid
sequenceDiagram
    autonumber
    participant C as Client
    participant API
    participant R as Redis
    participant W as Worker
    participant P as Postgres
    participant GW as Payment<br/>Gateway
    participant SW as Expiry<br/>Sweeper

    rect rgb(240, 248, 255)
    Note over C,P: 預訂(D1-D3、D4.1)
    C->>+API: POST /book {ticket_type_id, qty}
    API->>+R: Lua DECRBY + XADD<br/>(從 ticket_type 取價格快照)
    R-->>-API: ok
    API-->>-C: 202 {order_id, status:reserved,<br/>reserved_until, links.pay}
    R-->>W: XReadGroup
    W->>P: INSERT order status=awaiting_payment<br/>(amount_cents + currency 凍結)
    end

    alt 客戶在 TTL 內付款(D4-D5)
        rect rgb(245, 255, 245)
        C->>+API: POST /orders/:id/pay
        API->>+GW: CreatePaymentIntent
        GW-->>-API: client_secret
        API-->>-C: client_secret
        Note over C,GW: 客戶端完成 Stripe Elements
        GW->>+API: POST /webhook/payment (signed)
        API->>API: 驗簽 + 冪等檢查
        API->>P: status awaiting_payment → paid
        API-->>-GW: 200
        end
    else TTL 過期未付款(D6)
        rect rgb(255, 245, 245)
        SW->>P: SELECT awaiting_payment<br/>WHERE reserved_until <= NOW() - grace
        P-->>SW: 過期的列
        SW->>P: UoW {MarkExpired + emit order.failed}
        Note over P,R: outbox relay → Kafka order.failed →<br/>saga consumer + compensator<br/>(D7 後在 app 行程內)<br/>跑 revert.lua INCRBY → MarkCompensated
        end
    end
```

D6 的職責是時序 — 何時讓 reservation 過期。庫存回補由 saga compensator 負責(透過 `saga:reverted:order:<id>` SETNX 保證冪等)。形狀跟 D5 失敗路徑一樣;D6 不會直接呼叫 `revert.lua`。

**已上線:** [`v0.5.0`](https://github.com/Leon180/booking_monitor/releases/tag/v0.5.0)(2026-05-07,D1–D6)讓上面的「預訂 → 付款 → 過期」迴圈完整可跑。[`v0.6.0`](https://github.com/Leon180/booking_monitor/releases/tag/v0.6.0)(2026-05-08)把 saga compensator 的範圍收窄(D7 — 刪掉舊的 A4 自動扣款路徑;`order.failed` topic 上現在只剩 D5 webhook + D6 sweeper 兩個生產端 emitter),並出版 [瀏覽器 demo](demo/)(D8-minimal)。

## 特色

- **雙層庫存**:Redis(熱路徑,次毫秒等級) + PostgreSQL(事實來源)
- **非同步處理**:Redis Streams consumer group,含 PEL 恢復機制
- **Transactional Outbox**:訂單與事件同一筆交易,再由 OutboxRelay 發布至 Kafka
- **Saga 補償**:冪等地回滾付款失敗 + 預訂過期(DB + Redis)
- **四層冪等性**:API(`Idempotency-Key` header + N4 fingerprint 驗證)、Worker(DB UNIQUE 索引)、Saga(Redis SETNX)、付款閘道(mock gateway 實作 idempotent `CreatePaymentIntent`)
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

4. **重置狀態**(測試用)
   ```bash
   make reset-db
   ```

## 瀏覽器 Demo(D8-minimal)

[demo/](demo/) 底下的單頁 Vite + React + TS 應用在已跑起來的 stack 上,把整套 Pattern A 流程(book → pay-or-let-expire → terminal)端到端跑一輪。**僅 mock** — confirm 那一步走 `POST /test/payment/confirm/:order_id`(伺服器端偽造一筆已簽章的 webhook),沒有真的接 Stripe gateway。真 Stripe 整合留給 D4.2。

```bash
# 1. 把 API stack 跟 CORS、test endpoints 一起開起來
# APP_ENV=development 必設:docker-compose.yml 會把
# APP_ENV=${APP_ENV:-production} 傳進 container,而這個 env 值會
# 覆蓋 config/config.yml(cleanenv 優先序:env > yaml)。沒設的話
# container 會以 production 啟動,然後拒絕 ENABLE_TEST_ENDPOINTS。
export APP_ENV=development
export CORS_ALLOWED_ORIGINS=http://localhost:5173,http://127.0.0.1:5173
export ENABLE_TEST_ENDPOINTS=true
export PAYMENT_WEBHOOK_SECRET=demo_secret_local_only
docker compose up -d

# 2. 啟動 demo 的 dev server
cd demo
npm install   # 第一次跑才需要 — Node ≥ 20.19
npm run dev   # http://localhost:5173
```

完整流程說明、intent-aware 顯示的設計理由([demo/src/intent.ts](demo/src/intent.ts))與 `(intent, observed_status) → display` 對應表都在 [demo/README.md](demo/README.md)。

## 終端機 walkthrough(D10-minimal)

3 分鐘的終端機 walkthrough,跑完 Pattern A 三條完整路徑 — happy path、payment-failed、abandon→expiry。錄影檔已 commit 在 [`docs/demo/walkthrough.cast`](docs/demo/walkthrough.cast);播放用 `asciinema play docs/demo/walkthrough.cast`(先 `brew install asciinema`)。播放跟重錄的步驟請見 [`docs/demo/README.md`](docs/demo/README.md)。

從零重現:

```bash
make demo-up                          # 啟動 stack(20s reservation + 5s sweep)
scripts/d10_demo_walkthrough.sh        # 三階段 walkthrough
# 或重錄:
asciinema rec docs/demo/walkthrough.cast --overwrite \
    --command='scripts/d10_demo_walkthrough.sh'
```

架構脈絡(§5 forward recovery / §4 backward recovery、D5 webhook vs D6 sweeper 作為取消觸發來源)請見 [`docs/blog/2026-05-saga-pure-forward-recovery.zh-TW.md`](docs/blog/2026-05-saga-pure-forward-recovery.zh-TW.md)。對應的 k6 baseline benchmark(在 load 下跑同一條 two-step flow)請見 [`make bench-two-step`](Makefile) 跟 [`docs/benchmarks/`](docs/benchmarks/)。

## API 端點

| Method | Path | 說明 |
|--------|------|------|
| POST | `/api/v1/book` | 提交訂票。回 **202 Accepted** + 一個 `order_id` 用來追蹤訂單(詳見下方「**訂票流程**」)。 |
| GET | `/api/v1/orders/:id` | 用 `order_id` 查單筆訂單的最新狀態。在 `POST /book` 之後的短暫視窗裡會回 404(詳見「**訂票流程**」)。 |
| POST | `/api/v1/orders/:id/pay` | **D4** Pattern A — 為一筆 reservation 建立 Stripe-shape `PaymentIntent`。回 200 並帶 `{order_id, payment_intent_id, client_secret, amount_cents, currency}`。對 `order_id` 是冪等的(由 gateway 端保證)。狀態守門:non-`awaiting_payment` 回 409、reservation 已過期回 409、找不到 order 回 404。 |
| GET | `/api/v1/history` | 訂單歷史 `?page=1&size=10&status=confirmed` |
| POST | `/api/v1/events` | 建立活動 `{ name, total_tickets, price_cents, currency }`。D4.1 起會在同一筆交易中自動建立帶有價格快照的預設 ticket_type;response 的 `ticket_types[].id` 即為訂票時要帶的 id。 |
| GET | `/api/v1/events/:id` | **Stub** — 回 `{"message": "View event", "event_id": ...}` 並遞增 `page_views_total` 給轉換率追蹤。**不會**載入活動詳情(延後到 Phase 3 demo 才實作)。 |
| POST | `/webhook/payment` | **D5** Pattern A — 收 payment provider 的入站 webhook(Stripe-shape envelope)。用 `PAYMENT_WEBHOOK_SECRET` 做 HMAC-SHA256 簽章驗證,依 event type 分派:`payment_intent.succeeded` → MarkPaid;`payment_intent.payment_failed` → MarkPaymentFailed + emit `order.failed`(交給 saga 補償)。對 provider 重投靠資料庫終態做冪等。掛在 engine root(**不**在 `/api/v1` 之下);驗證身份靠簽章,不靠網路。 |
| POST | `/test/payment/confirm/:order_id` | **D5(僅測試用)** 模擬 provider 端的 webhook emit — 由 `ENABLE_TEST_ENDPOINTS` 控制(prod 預設關)。讀取 order 的 `payment_intent_id`,組出帶 `metadata.order_id` 的 Stripe-shape envelope,用同一把 webhook secret 簽名,然後 POST 到 `/webhook/payment`。整合測試與 dev demo 用來在沒有真 provider 的情況下走完整 pipeline。Query:`?outcome=succeeded\|failed`。 |
| GET | `/metrics` | Prometheus 指標 |
| GET | `/livez` | Liveness 探針 — process 還活著就一律回 200(不依賴下游) |
| GET | `/readyz` | Readiness 探針 — PG + Redis + Kafka 都在 1s 內回應才回 200,否則回 503 並附逐 dep 的 JSON |

### 訂票流程

`POST /api/v1/book` 在設計上**就是非同步的**。回 202(不是 200)是誠實的:在這個回應的當下,只完成了 Redis 端的庫存扣減 — 訂單還沒寫進資料庫、付款還沒嘗試、訂票其實還沒真正成功。Client 拿到 `order_id` 之後,要自己輪詢最終狀態。

```
1. Client → POST /api/v1/book { user_id, ticket_type_id, quantity }
2. Server → 202 Accepted {
       order_id:           "019dd493-47ae-79b1-b954-8e0f14a6a482",
       status:             "reserved",
       message:            "booking accepted, complete payment before reserved_until",
       reserved_until:     "2026-05-04T22:30:00Z",
       expires_in_seconds: 900,
       links: {
         self: "/api/v1/orders/019dd493-...",
         pay:  "/api/v1/orders/019dd493-.../pay"
       }
   }

   此時:
   - Redis 庫存:已扣減(這是 load-shed gate)
   - DB orders 列:還沒寫入(worker 在 ~ms 後寫成 awaiting_payment,
                              並把 amount_cents + currency 快照凍結在該列上)
   - 付款:還沒嘗試 — client 必須在 reserved_until 之前 POST links.pay,
                      否則 D6 expiry sweeper 會把該列翻成 "expired" 並退還庫存
   - 結果:還不知道

3. Client → GET /api/v1/orders/<order_id>  (帶 backoff 輪詢:100ms → 250ms → 500ms ...)

   可能的回應:
   - 404  → worker 還沒寫進 DB。重試即可。
   - 200  → { id, user_id, ticket_type_id, quantity, amount_cents, currency,
              status, reserved_until, created_at }
            其中 `status` 為:
              "awaiting_payment" — DB 已寫入,等待在 reserved_until 前 POST /pay
              "charging"         — PaymentIntent 已建立,付款進行中
              "paid"             — 付款成功 + 訂票完成               ✓ 終態(成功)
              "expired"          — reserved_until 過期且未付款;
                                    庫存已退還                       ✓ 終態(逾時)
              "failed"           — 付款失敗,saga 即將回滾
              "compensated"      — saga 已回滾庫存                  ✓ 終態(失敗)
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
| app | 8080 | Booking API server(同時 in-process 跑 `order.failed` 的 saga consumer) |
| nginx | 80 | Reverse proxy + 限流 |
| recon | - | 卡單收尾的 Reconciler 子指令(`booking-cli recon`) |
| saga_watchdog | - | 卡在 `failed` 狀態的訂單 DB-side sweep(`booking-cli saga-watchdog`) |
| expiry_sweeper | - | D6 reservation 到期 sweeper(`booking-cli expiry-sweeper`) |
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
- [Browser Demo](demo/README.md) — **D8-minimal** Vite + React + TS 應用:book → pay → confirm/let-expire 端到端
- [Project Review Checkpoints](docs/checkpoints/) — 各個 phase 邊界的全專案審計報告
- [Scaling Roadmap](docs/scaling_roadmap.md) — 歷史性的 Stage 1-4 架構演進敘事
- [Architecture (Current)](docs/architecture/current_monolith.md) — 目前架構的 Mermaid 圖
- [Architecture (Future)](docs/architecture/future_robust_monolith.md) — 目標架構
- [ADR-001: Queue Selection](docs/adr/0001_async_queue_selection.md) — Redis Streams vs Kafka 決策
- [Phase 2 Review](docs/reviews/phase2_review.md) — 早期 phase 的 Redis 整合 review
- [Benchmarks](docs/benchmarks/) — 歷次效能比對報告
