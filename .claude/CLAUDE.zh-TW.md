# Booking Monitor - Claude Code 專案指南

> English version: [CLAUDE.md](CLAUDE.md)

## ⚠️ 雙語文件同步契約(必須遵守)

本專案有三份文件同時維護**英文版與繁體中文版**,這些檔案被視為**單一邏輯單元**。修改其中任何一份時,必須在**同一次回覆**內同時更新兩個語言版本,並保持結構完全一致(相同章節、相同表格、相同順序)。

**配對檔案(相對於 repo 根目錄):**
| 英文 | 中文 |
|------|------|
| `.claude/CLAUDE.md` | `.claude/CLAUDE.zh-TW.md` |
| `README.md` | `README.zh-TW.md` |
| `docs/PROJECT_SPEC.md` | `docs/PROJECT_SPEC.zh-TW.md` |

**規則:**
1. **絕不**只修改配對檔案的其中一邊。無法翻譯時請先問使用者,不要略過。
2. **結構對等**:章節標題、表格列、順序必須一對一對應。
3. **程式碼、指令、檔名保持英文**,只翻譯說明文字。
4. **新增章節時**,兩個版本要在同一次回覆內、相同位置新增。
5. **使用者要求修改其中一份時**,先告知配對檔案也會一併更新,完成兩邊後才算結束任務。
6. **翻譯風格**:中文版使用台灣繁體中文用語(例:資料庫、介面、物件,非簡中用語)。

**備註**:Claude Code 會以相同的優先權自動載入 `./CLAUDE.md` 與 `./.claude/CLAUDE.md`,因此本檔(`.claude/CLAUDE.md`)會被直接載入,不需要 root stub。只有英文版會被自動載入,`.claude/CLAUDE.zh-TW.md` 純粹是給人看的參考。PostToolUse hook(`.claude/hooks/check_bilingual_docs.sh`)會在編輯時強制執行這份契約。

---

## 專案概述
高並發票務訂票系統(Flash Sale 模擬器),以 Go 撰寫,採用 DDD + Clean Architecture 的模組化單體(Modular Monolith)。雙層庫存機制(Redis 熱路徑 + PostgreSQL 為事實來源),透過 Redis Streams 做非同步處理,Kafka Outbox Pattern 發布事件,並以 Saga 模式處理付款失敗補償。

## 技術棧
Go 1.24 | Gin | PostgreSQL 15 | Redis 7 | Kafka | Prometheus | Grafana | Jaeger | Nginx

## 架構分層
```
internal/
  domain/         # 實體 (Event, Order),介面 (repositories, services)
  application/    # 服務: BookingService, WorkerService, OutboxRelay, SagaCompensator, PaymentService
  infrastructure/ # 適配器: api/, cache/, persistence/postgres/, messaging/, observability/, payment/, config/
  mocks/          # 產生的 mock 檔 (go.uber.org/mock)
```

## 關鍵指令
```bash
make build          # 以 -race 建置 binary
make test           # 跑測試含 race detection
make run-server     # 啟動 API server(port 8080)
make run-stress     # 壓測(C=並發量,N=請求數)
make stress-k6      # K6 壓測 (VUS=500, DURATION=30s)
make reset-db       # 清空訂單,庫存重設為 100
make migrate-up     # 執行資料庫 migration
make mocks          # 重新產生 mock 檔
docker-compose up -d  # 啟動完整環境(app, nginx, payment_worker, postgres, redis, kafka, prometheus, grafana, jaeger)
```

## 核心設計模式
- **Transactional Outbox**:Order + OutboxEvent 在同一個 DB transaction 內寫入 → OutboxRelay 輪詢 → 發布至 Kafka
- **Saga 補償**:付款失敗 → `order.failed` topic → SagaCompensator 回滾 DB 與 Redis 庫存
- **Unit of Work**:透過 context 注入 transaction,實作於 `PostgresUnitOfWork`
- **Advisory Lock**:PostgreSQL `pg_try_advisory_lock(1001)` 讓 OutboxRelay 做領導者選舉
- **Redis Lua Script**:`deduct.lua`(原子扣減庫存 + 發布至 Stream),`revert.lua`(冪等補償)
- **冪等性**:API 層(Idempotency-Key header)、Worker 層(DB 唯一索引)、Saga 層(Redis SETNX)

## API 端點
```
POST /api/v1/book          # 訂票 (user_id, event_id, quantity)
GET  /api/v1/history       # 分頁查詢訂單 (?page=&size=&status=)
POST /api/v1/events        # 建立活動 (name, total_tickets)
GET  /api/v1/events/:id    # 查看活動
GET  /metrics              # Prometheus 指標
```

舊版 `POST /book` 已於 Phase 13 remediation(PR #9 H9)移除 — 它會繞過 nginx 的限流 zone。所有呼叫端必須改用 `/api/v1/book`。

## 資料庫
- PostgreSQL 對外 port 5433(user/password/booking)
- 3 張資料表:`events`, `orders`, `events_outbox`
- 7 個 migration 檔位於 `deploy/postgres/migrations/`(PR #12 新增 000007:`events_outbox(id) WHERE processed_at IS NULL` 的部分索引)

## Kafka Topics
- `order.created` — 由 payment service 消費(group `payment-service-group`)
- `order.created.dlq` — 無法解析 / invalid payment event 的 dead letter
- `order.failed` — 由 saga compensator 消費(group `booking-saga-group`)
- `order.failed.dlq` — saga 事件超過 `sagaMaxRetries=3` 後的 dead letter

Group ID 與 topic 名稱皆可透過 `KAFKA_PAYMENT_GROUP_ID`、`KAFKA_ORDER_CREATED_TOPIC`、`KAFKA_SAGA_GROUP_ID`、`KAFKA_ORDER_FAILED_TOPIC` 設定。

## 開發規範
- **Immutable 資料模式**:建立新物件,不要 mutate
- 檔案 < 800 行;function 預設 < 50 行。Bootstrap / DI 組裝 / 線性構造類程式碼(例如 `cmd/booking-cli/main.go` 的 fx.Invoke 主體)在拆分只會增加 indirection、卻不會讓意圖更清楚時,可放寬到 ~80 行。不要純粹為了湊行數而抽 helper。
- 每一層都要明確處理 error
- 所有邊界都要驗證輸入
- 不得硬編密碼/金鑰,一律用環境變數
- 測試使用 testify/assert + go.uber.org/mock

## Benchmark 慣例

效能 regression 追蹤都放在 `docs/benchmarks/`。目錄結構與內容是約定俗成,並非工具強制 — 維持一致才能讓歷史 run 之間可比較。

**目錄命名**:`YYYYMMDD_HHMMSS_compare_c<vus>[_<tag>]`(例:`20260426_183530_compare_c500_pr35`)。`c<vus>` 段表示 VU 數量;尾端的 `_<tag>` 用來描述比較的對象(PR 編號、phase 名稱等),非必填。

**每個目錄必備檔案**:
- `comparison.md` — 參數、run A vs run B 的指標表、結論、注意事項(範本參考 `20260426_183530_compare_c500_pr35/comparison.md`)
- `run_a_raw.txt` — baseline run 的完整 k6 stdout
- `run_b_raw.txt` — 受測 run 的完整 k6 stdout

**Apples-to-apples 標準條件**(除非刻意要量別的東西,否則一律用以下設定):
- Script:`scripts/k6_comparison.js`
- VUs:500
- Duration:60s
- 票池:500,000(讓整個 run 都不會 sold-out — 測純容量)
- 目標:`http://app:8080/api/v1`(直連,繞過 nginx 限流)
- 兩個 run 都在同一台主機、等價的 Docker 環境

**何時要留 report**:會動到訂票 hot path 的 PR(handler / `BookingService.BookTicket` / Redis Lua / `OrderMessageProcessor.Process` 的 tx 主體 / outbox relay 輪詢)**必須**附上比對 report。純重構若 diff 可以證明 hot path 一個 byte 都沒改(例:PR 31→35 就是觸發這條規則的案例)**可以**跳過,但附一份「驗證無 regression」的 report 仍然是最乾淨的證據、也是優先選擇。

**現有工具**:`make benchmark-compare VUS=500 DURATION=60s` 會跑 `scripts/benchmark_compare.sh`,自動建立目錄與 raw 輸出;`comparison.md` 再由人撰寫,引用已存下的 raw 檔。k6-on-Docker laptop 的 run-to-run 變異通常落在 3-5%,低於這個區間的 delta 是 noise 不是信號。

## 目前狀態(截至 2026-04-24)
已完成 15 個階段。Phase 13(4/11)透過 PR #7/#8/#9/#12/#13 修復了 66 項 review findings;Phase 14(4/12–13)在 PR #14/#15 完成 GC 優化 — pprof harness、sampler 調優、`GOGC=400`、`GOMEMLIMIT=256MiB`、Redis Lua args 的 sync.Pool、合併版 middleware(每個 request 僅 1 次 `context.WithValue` + 1 次 `c.Request.WithContext`)。Phase 15(4/23–24)在 PR #18 完成 logger 架構重構 — `pkg/logger/` → `internal/log/`,新增 ctx-aware emit 方法(`Debug/Info/Warn/Error/Fatal(ctx, msg, fields...)`)會自動注入 `correlation_id` 與 OTEL `trace_id`/`span_id`,在 pprof listener 上掛載 `/admin/loglevel` runtime level 端點,並提供 `internal/log/tag/` 型別化欄位建構子。Phase 15 後(4/24)於 PR #21/#22/#23 做 review cleanup:對 `cmd/booking-cli/main.go` 做嚴格 review,修復 P0 的 stress URL、payment worker 補 tracer init、pprof 改綁 loopback + 加 timeout、goroutine 失敗時透過 `fx.Shutdowner` 升級關機、function 拆分,並把 pprof / trusted-proxies / db-ping 調整項搬到 `config.Config`(`KAFKA_BROKERS` 與 `TRUSTED_PROXIES` 改為 cleanenv 的 `[]string` + `env-separator`,順便修掉一個 multi-broker 靜默失效的 bug)。完整規格見 [../docs/PROJECT_SPEC.zh-TW.md](../docs/PROJECT_SPEC.zh-TW.md)。

## Logging 使用慣例(PR #18 之後)
- **Pattern A — 長生命週期元件**:透過 constructor 注入 `*log.Logger`,在建構時一次性用 `With()` 加上 `component=<subsystem>` 標籤(例:`worker_service`、`outbox_relay`、`saga_compensator`)。呼叫 `l.Error(ctx, "msg", tag.OrderID(id))` — ctx-aware 方法會自動注入 correlation/trace ids。
- **Pattern B — 呼叫點本地程式碼**:handlers、middleware、init 路徑用 package-level `log.Error(ctx, "msg", tag.UserID(uid))`。會透過 `FromContext` 從 ctx 讀取 logger,未設定時 fallback 到 Nop。
- **型別化欄位**:優先使用 `internal/log/tag/` 的 `tag.OrderID`/`tag.Error`/`tag.UserID` 等,不要寫原始 `log.Int("order_id", ...)` — 編譯期檢查拼字錯誤。
- **One-off inline 欄位**:不值得建 typed tag 的 key(`component`、`batch_size`、`payload` 等)使用 `internal/log/` 的 `log.String`/`log.Int`/`log.Int64`/`log.ByteString`/`log.Err`/`log.NamedError`。應用程式碼**不要**直接 import `go.uber.org/zap` — zap 封裝在 `internal/log/` 內部。
- **永遠不要呼叫 `zap.S()` 或 `zap.L()` 全域** — 這個專案沒有 wire 全域 logger,所有地方都用 DI 注入。

## 待辦路線圖
- **DLQ Worker**(優先):dead letter 的重試政策(目前訊息進了 `orders:dlq` 就沒有重跑機制)
- **Event Sourcing / CQRS**:append-only event store + 讀寫分離
- **水平擴展測試**:多實例部署驗證
- **真實支付閘道**:目前只有 mock
- **管理後台**:目前沒有管理 UI

## 關鍵環境變數
完整清單見 [docs/PROJECT_SPEC.zh-TW.md § 7](../docs/PROJECT_SPEC.zh-TW.md)。最常用的幾組:

**Runtime / GC / Tracing**
- `GOGC`(`.env` 預設 `400`,docker-compose fallback `100`)— 數值越高 GC 觸發越少
- `GOMEMLIMIT=256MiB` — 軟記憶體上限;搭配 GOGC 使用,只有在接近上限時才變積極
- `OTEL_TRACES_SAMPLER_RATIO`(預設 `0.01`)— 採樣 1%;`1` = 永遠採樣,`0` = 全不採樣

**安全敏感(PR #21/#22 之後)**
- `ENABLE_PPROF`(預設 `false`)— 設為 `true` 時啟動 pprof + `/admin/loglevel` listener
- `PPROF_ADDR`(預設 `127.0.0.1:6060`)— **預設綁 loopback**;僅在真的需要遠端 pprof 時才覆寫。Heap dump 與 log level 調整都掛在這裡
- `TRUSTED_PROXIES`(CSV,預設 RFC1918 CIDR)— Gin 做 `ClientIP()` 時信任的 CIDR;RFC1918 外的 service mesh(GKE、部分 EKS)要覆寫

**維運相關(PR #21/#22 之後)**
- `CONFIG_PATH`(預設 `config/config.yml`)— config 檔路徑;CWD 不同時(systemd、k8s initContainer)需要覆寫
- `DB_PING_ATTEMPTS` / `DB_PING_INTERVAL` / `DB_PING_PER_ATTEMPT` — DB 啟動探測預算;依賴較慢時拉高 attempts
- `KAFKA_BROKERS`(CSV,預設 `localhost:9092`)— 現在透過 cleanenv 的 `env-separator:","` parse 成 `[]string`

**Worker / Cache(PR #37 之後)**
- `WORKER_STREAM_READ_COUNT`(預設 `10`)/ `WORKER_STREAM_BLOCK_TIMEOUT`(預設 `2s`)— 訂單 stream consumer 的 XReadGroup 批次大小與 block 時長
- `WORKER_MAX_RETRIES`(預設 `3`)/ `WORKER_RETRY_BASE_DELAY`(預設 `100ms`)— 每則訊息的 retry 預算 + 線性 backoff 基數;確定性失敗(deterministic failure)的錯誤透過 application 層的 retry policy 直接 bypass 預算
- `WORKER_FAILURE_TIMEOUT`(預設 `5s`)— handleFailure 補償流程的 ctx 預算(Redis revert + DLQ XAdd)
- `WORKER_PENDING_BLOCK_TIMEOUT`(預設 `100ms`)/ `WORKER_READ_ERROR_BACKOFF`(預設 `1s`)— 啟動時 PEL 掃描的 block 時長 + 讀取錯誤的 retry 間隔
- `REDIS_INVENTORY_TTL`(預設 `720h`)/ `REDIS_IDEMPOTENCY_TTL`(預設 `24h`)— Redis 快取 key 的存活期;以前是寫死的 const
- `REDIS_MAX_CONSECUTIVE_READ_ERRORS`(預設 `30`)— Redis 持續異常時 worker 退出讓 k8s 重啟的容忍度

## `.claude/` 下的可用工具

Claude Code 會自動探索 `.claude/agents/` 與 `.claude/skills/` 下的資產。以下工具採自 [affaan-m/everything-claude-code](https://github.com/affaan-m/everything-claude-code)(MIT),完整來源清單見 [.claude/ATTRIBUTIONS.md](ATTRIBUTIONS.md)。

### Subagents(`.claude/agents/`)
- **go-reviewer** — TRIGGER:PR 內有任何 `*.go` 檔異動。檢查安全性(SQL/command injection、race condition、`InsecureSkipVerify`)、錯誤處理(wrapping、`errors.Is/As`)、併發(goroutine leak、channel deadlock)以及程式碼品質。
- **go-build-resolver** — TRIGGER:`go build` 或 `go test` 失敗。診斷 import cycle、版本不一致、模組錯誤。
- **silent-failure-hunter** — TRIGGER:review 錯誤處理路徑,尤其是 Kafka consumers([internal/infrastructure/messaging/](../internal/infrastructure/messaging/))、outbox relay([internal/application/outbox_relay.go](../internal/application/outbox_relay.go))、saga compensator、worker service。專門獵捕被吞掉的 error、空的 catch 區塊、錯誤的 fallback。

### Skills(`.claude/skills/`)
- **golang-patterns** — TRIGGER:撰寫新的 Go 程式碼。Go 慣用寫法:小型介面、錯誤 wrapping、context 傳遞。
- **golang-testing** — TRIGGER:補測試。Table-driven tests、`testify` / `go.uber.org/mock`、race detection、覆蓋率。
- **postgres-patterns** — TRIGGER:動到 `internal/infrastructure/persistence/postgres/` 或 migration。交易、advisory lock、索引、連線池。
- **tdd-workflow** — TRIGGER:開始新的 feature / bugfix。Red-green-refactor 循環,把全域 coding style 裡面的 TDD 規範落實成可執行步驟。

### Rules(`.claude/rules/golang/`)
在使用者的全域 `~/.claude/rules/common/` 之上,加入 Go 專用的標準: `coding-style.md`, `hooks.md`, `patterns.md`, `security.md`, `testing.md`。
