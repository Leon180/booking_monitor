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
POST /book                 # 舊版 route(Phase 0 保留)
```

## 資料庫
- PostgreSQL 對外 port 5433(user/password/booking)
- 3 張資料表:`events`, `orders`, `events_outbox`
- 6 個 migration 檔位於 `deploy/postgres/migrations/`

## Kafka Topics
| Topic | Producer | Consumer Group | Consumer |
|-------|----------|----------------|----------|
| `order.created` | OutboxRelay | `payment-service-group-test` | PaymentWorker |
| `order.failed` | PaymentWorker (透過 outbox) | `booking-saga-group` | SagaCompensator |

## 開發規範
- **Immutable 資料模式**:建立新物件,不要 mutate
- 檔案 < 800 行,function < 50 行
- 每一層都要明確處理 error
- 所有邊界都要驗證輸入
- 不得硬編密碼/金鑰,一律用環境變數
- 測試使用 testify/assert + go.uber.org/mock

## 目前狀態(截至 2026-02-24)
已完成 12 個階段。核心系統完整運作:雙層庫存、非同步處理、Outbox、付款流程、Saga 補償。完整規格見 [../docs/PROJECT_SPEC.zh-TW.md](../docs/PROJECT_SPEC.zh-TW.md)。

## 待辦路線圖
- **DLQ Worker**(優先):dead letter 的重試政策(目前訊息進了 `orders:dlq` 就沒有重跑機制)
- **Event Sourcing / CQRS**:append-only event store + 讀寫分離
- **水平擴展測試**:多實例部署驗證
- **真實支付閘道**:目前只有 mock
- **管理後台**:目前沒有管理 UI

## `.claude/` 下的可用工具

Claude Code 會自動探索 `.claude/agents/` 與 `.claude/skills/` 下的資產。以下工具採自 [affaan-m/everything-claude-code](https://github.com/affaan-m/everything-claude-code)(MIT),完整來源清單見 [.claude/ATTRIBUTIONS.md](ATTRIBUTIONS.md)。

### Subagents(`.claude/agents/`)
- **go-reviewer** — 修改任何 Go 程式碼時使用。檢查安全性(SQL/command injection、race condition、`InsecureSkipVerify`)、錯誤處理(wrapping、`errors.Is/As`)、併發(goroutine leak、channel deadlock)以及程式碼品質。
- **go-build-resolver** — `go build` 或 `go test` 失敗時使用。診斷 import cycle、版本不一致、模組錯誤。
- **silent-failure-hunter** — review 錯誤處理路徑時使用,尤其是 Kafka consumers([internal/infrastructure/messaging/](../internal/infrastructure/messaging/))、outbox relay([internal/application/outbox_relay.go](../internal/application/outbox_relay.go))、saga compensator、worker service。專門獵捕被吞掉的 error、空的 catch 區塊、錯誤的 fallback。

### Skills(`.claude/skills/`)
- **golang-patterns** — Go 慣用寫法:小型介面、錯誤 wrapping、context 傳遞。
- **golang-testing** — Table-driven tests、`testify` / `go.uber.org/mock`、race detection、覆蓋率。
- **postgres-patterns** — PG 專屬:交易、advisory lock、索引、連線池。
- **tdd-workflow** — Red-green-refactor 循環,把全域 coding style 裡面的 TDD 規範落實成可執行步驟。

### Rules(`.claude/rules/golang/`)
在使用者的全域 `~/.claude/rules/common/` 之上,加入 Go 專用的標準: `coding-style.md`, `hooks.md`, `patterns.md`, `security.md`, `testing.md`。
