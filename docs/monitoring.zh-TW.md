# 監控指南

> English version: [monitoring.md](monitoring.md)

這份文件是日常運維者的參考 — **stack 跑起來後要怎麼觀察 `booking_monitor`**。它**不是**架構規格(那部分看 [PROJECT_SPEC.zh-TW.md](PROJECT_SPEC.zh-TW.md))— 它要回答的是具體的問題:「現在系統健康嗎」、「這個指標定義在哪」、「我要怎麼故意把這條告警觸發起來測試」。

三個觀測性介面:

| 介面 | URL | 提供什麼 |
| :-- | :-- | :-- |
| **原始 `/metrics`** | http://localhost:80/metrics(經 nginx)或 http://localhost:8080/metrics(直接打 app) | Prometheus exposition format。便宜、可被腳本化、適合用 `grep` / `curl` 做正確性檢查。 |
| **Prometheus UI** | http://localhost:9090 | 臨時 PromQL 查詢、target 健康度、告警狀態。 |
| **Grafana** | http://localhost:3000(帳密 `admin` / `admin`) | 預先配置好的儀表板。「現在有沒有東西在閃紅燈」的最佳介面。 |

`/livez` 和 `/readyz` 也分別在 http://localhost:80/livez 與 `/readyz` 提供,給 Kubernetes 風格的健康探測使用 — protocol 合約見 [PROJECT_SPEC.zh-TW.md §6](PROJECT_SPEC.zh-TW.md)。

---

## 1. 快速健康檢查(60 秒迴圈)

要快速知道「現在系統健康嗎」:

```bash
# 1. App process 還活著嗎?
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:80/livez            # → 200

# 2. App 看得到所有依賴嗎?
curl -s http://localhost:80/readyz | jq                                       # → status: ok

# 3. 有沒有正在 firing 的告警?
curl -s http://localhost:9090/api/v1/alerts | jq '.data.alerts[] | {name: .labels.alertname, state, value}'

# 4. 訂票熱路徑有流量嗎?有成功嗎?
curl -s http://localhost:80/metrics | grep -E '^bookings_total\{' | head

# 5. 從 id 一路追單筆訂單到最終狀態(PR #47)
ORDER_ID="<從 POST /book 的 response 貼進來>"
curl -s "http://localhost:80/api/v1/orders/$ORDER_ID" | jq
# → {"id":"...", "status":"confirmed", ...}
# 在短暫的非同步處理視窗會回 404 — 帶 backoff 重試即可。
```

五個任何一個吐出非預期結果,就往下面對應的更深層介面鑽。

---

## 2. 指標清單

權威來源是 `internal/infrastructure/observability/metrics.go`,加上兩個 collector(`db_pool_collector.go`、`streams_collector.go`)。完整有註解的列表在 [PROJECT_SPEC.zh-TW.md §7](PROJECT_SPEC.zh-TW.md)。下面是按「你會問什麼問題」做的實用分組。

### 每個 request — RED 方法(Rate / Errors / Duration)

| 問題 | 指標 | 範例查詢 |
| :-- | :-- | :-- |
| 流量多大? | `http_requests_total{method,path,status}` | `sum by (status) (rate(http_requests_total[1m]))` |
| 多快? | `http_request_duration_seconds_bucket` | `histogram_quantile(0.99, sum(rate(http_request_duration_seconds_bucket[5m])) by (le))` |
| 多少 % 失敗? | 同上,過濾 `status=~"5.."` | `sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m]))` |

### 每個資源 — USE 方法(Utilization / Saturation / Errors)

| 資源 | 指標前綴 | 備註 |
| :-- | :-- | :-- |
| Go runtime | `go_*`、`process_*` | goroutines、GC pause、heap inuse — 透過 `collectors.NewGoCollector` 註冊 |
| DB pool | `db_pool_*` | `db_pool_in_use`、`db_pool_idle`、`db_pool_wait_count`、`db_pool_wait_duration_seconds` |
| Redis 快取(Go-client 視角) | `cache_hits_total{cache}`、`cache_misses_total{cache}`、`cache_errors_total{cache,op}` | 每個快取名稱獨立的 hit/miss;**我們的應用程式**所看到的。`cache_errors_total` 是「Redis 掛了」的操作員訊號 — 跟 `cache_misses_total` 拆開,讓 Redis 故障不會偽裝成 hit-rate dashboard 上的 cache-cold spike。`op` ∈ {get, set, marshal}。 |
| Redis streams | `redis_stream_length{stream}`、`redis_stream_pending_entries{stream,group}`、`redis_stream_consumer_lag_seconds{stream,group}` | scrape 時由 `StreamsCollector` 即時讀取 |
| Redis server(oliver006 exporter,從 `redis_exporter:9121` scrape) | `redis_*` | 「Redis 自己飽和了嗎」這類我們應用層指標無法回答的問題。請參考下方子表格。 |
| Redis client pool(go-redis `PoolStats()`,從 app `:8080/metrics` scrape) | `redis_client_pool_*` | 「**我們的 client** 是不是在排隊等連線」這個視角。跟 `db_pool_*` 是兄弟。請參考下方子表格。 |

**Redis 伺服器端指標(來自 `redis_exporter`)** — 補上 Go-client 端 `cache_*` 與 `redis_stream_*` 指標無法回答的盲點。triage「為什麼訂票熱路徑變慢」時非常有用:

| 問題 | 指標 | 範例查詢 |
| :-- | :-- | :-- |
| Redis 主執行緒 CPU 飽和了嗎? | `redis_cpu_sys_seconds_total`、`redis_cpu_user_seconds_total` | `rate(redis_cpu_sys_seconds_total[1m]) + rate(redis_cpu_user_seconds_total[1m])`(持續逼近 1.0 → 飽和) |
| 哪個指令在吃 Redis CPU? | `redis_commands_duration_seconds_total{cmd}` | `topk(5, rate(redis_commands_duration_seconds_total[5m]))` |
| 哪個指令呼叫頻率最高? | `redis_commands_total{cmd}` | `topk(5, rate(redis_commands_total[5m]))` |
| 特別針對 Lua eval 的延遲 | 在 duration 指標上過濾 `cmd=~"eval.*"` | `rate(redis_commands_duration_seconds_total{cmd=~"eval.*"}[5m]) / rate(redis_commands_total{cmd=~"eval.*"}[5m])`(平均每次呼叫的 μs) |
| SLOWLOG 長度 / 上一次慢操作耗時 | `redis_slowlog_length`、`redis_last_slow_execution_duration_seconds` | `redis_slowlog_length` 持續 > 30 秒非零 = 有東西穩定地慢 |
| 記憶體壓力 | `redis_memory_used_bytes`、`redis_memory_max_bytes`、`redis_mem_fragmentation_ratio` | fragmentation 持續 > 1.5 → 考慮 `MEMORY PURGE` 或重啟 |
| 連線端的 back-pressure | `redis_connected_clients`、`redis_blocked_clients` | `redis_blocked_clients > 0` 持續 1 分鐘 → 有 client 卡在 `BLPOP` / `XREAD` block 上 |
| Exporter 自己健康嗎? | `up{job="redis"}` | `up{job="redis"} == 0` 持續 1 分鐘 → exporter 或 Redis 本身連不到 |

Exporter 每次 scrape 跑一次 Redis `INFO` + `commandstats` + `SLOWLOG`,各只有單次 round-trip,實測對 Redis 沒有可觀的負載(由 `redis_exporter` 自己的 self-metrics 驗證)。完整指標清單請看 [oliver006/redis_exporter README](https://github.com/oliver006/redis_exporter#whats-exported)。

**Redis client 連線池指標(來自 `redis_pool_collector.go`)** — 配合 server 端 exporter 與應用層 `cache_*` 結果指標的第三條腿。回答「**我們的 Go redis client** 是不是在排隊等連線」 — 當 server 端看起來閒閒的時候,這就是最常見的飽和原因(Stack Overflow 公開的架構在 Redis CPU 2% 的條件下做到 87k cmd/s,client 端飽和才是「Redis 變慢」的典型原兇):

| 問題 | 指標 | 範例查詢 |
| :-- | :-- | :-- |
| 此刻連線池大小 + 佔用情況 | `redis_client_pool_total_conns`、`redis_client_pool_idle_conns`、`redis_client_pool_stale_conns` | gauge,不需要 rate |
| 連線重用率(快路徑) | `redis_client_pool_hits_total` | `rate(redis_client_pool_hits_total[1m])` |
| **PoolSize 太小**(被迫新建連線而非重用) | `redis_client_pool_misses_total` | `rate(redis_client_pool_misses_total[1m]) > 0` 持續 → 該調大 `cfg.Redis.PoolSize` |
| **連線池完全爆掉**(page-worthy 硬飽和) | `redis_client_pool_timeouts_total` | `rate(redis_client_pool_timeouts_total[1m]) > 0` 持續 1m → page |
| Goroutine 累計等待時間 | `redis_client_pool_wait_count_total`、`redis_client_pool_wait_duration_seconds_total` | `rate(redis_client_pool_wait_duration_seconds_total[1m])` ≈「每秒中有幾秒在等」;若值 > goroutine 數 × 幾 ms = 輕度爭用;若接近 goroutine 數 = 嚴重排隊 |
| 連線被當成壞掉而拋棄 | `redis_client_pool_unusable_total` | `rate(redis_client_pool_unusable_total[5m])` 非零 → server 端斷線(Redis 重啟、網路) |

Collector 在 scrape 當下讀 `*redis.Client.PoolStats()`(已經有鎖保護 + atomic load,很便宜)。模式跟 Postgres 的 `pg_pool_*` collector 完全一樣。

### 領域指標 — 業務在意的事

| 問題 | 指標 |
| :-- | :-- |
| 成功訂票 vs 售完 vs 重複下單 | `bookings_total{status}` |
| Worker 處理結果 | `worker_orders_total{status}`、`worker_processing_duration_seconds` |
| Redis 與 DB 庫存漂移 | `inventory_conflicts_total`(worker 端,Redis 通過但 DB 拒絕); `inventory_rehydrate_drift_total`(啟動時,Redis key 存在且值 > DB available_tickets — 見 [`cache/rehydrate.go`](../internal/infrastructure/cache/rehydrate.go))。多次部署後仍看到 `rate(inventory_rehydrate_drift_total[1h]) > 0` 持續 = 有人/事在把錯的值寫進 Redis(毀損、手動操作、或 `architectural_backlog.md` § Cache-truth architecture 紀錄的 NOGROUP 後遺症)。 |
| Dead-letter 路由 | `dlq_messages_total{topic,reason}`、`redis_dlq_routed_total{reason}` — `reason` 列舉:`malformed_classified`(handler invariant 拒絕)、`exhausted_retries`、`malformed_reverted_legacy`(parse 失敗但已透過 legacy hints 補償退還 Redis 庫存 — rolling-upgrade 預期會逐漸 taper)、`malformed_unrecoverable`(parse 失敗且 revert hints 無法解析,或 RevertInventory 自身失敗 — 庫存已洩漏,需要 page)。舊 `malformed_parse` label 保留 pre-warm 以相容舊 alert,但已不再有新 emit;新 alert 應使用更明確的 label。 |
| Saga 補償 poison 訊息 | `saga_poison_messages_total` |
| Kafka consumer 因下游短暫故障卡住 | `kafka_consumer_retry_total{topic,reason}` |
| 卡在 Charging 的訂單對帳 | `recon_stuck_charging_orders`(gauge)、`recon_resolved_total{outcome}`、`recon_gateway_errors_total`、`recon_resolve_duration_seconds`、`recon_resolve_age_seconds` |
| Streams collector 自己掛了 | `redis_stream_collector_errors_total{stream,operation}` |
| **Worker 自癒 NOGROUP(靜默訊息遺失訊號)** | `consumer_group_recreated_total` — counter,worker 遇到 NOGROUP 並透過 `XGROUP CREATE ... $` 重建消費者群組時 +1。**健康的 production 應該永遠是 0。** 重建用 `$` 表示「從目前 stream 尾端開始」,所以群組消失到重建之間進入 stream 的訊息會被靜默跳過。配對告警 `ConsumerGroupRecreated` 第一次發生就觸發(無 soak window)。背景:`docs/architectural_backlog.md` § Cache-truth architecture 紀錄了 1000 筆訊息中靜默遺失 411 筆的案例,就是這個 metric 要把它 surface 出來。 |
| **Transactional outbox 積壓** | `outbox_pending_count` — gauge,scrape 時由 [`OutboxPendingCollector`](../internal/infrastructure/observability/outbox_pending_collector.go) 取樣 `events_outbox` 中 `processed_at IS NULL` 的列數。Steady-state 個位數(in-flight 事件靠 OutboxRelay 在秒級內排空)。持續 > 100 = relay 卡住 → saga 補償器 + 付款服務這些下游消費者全部沉默,in-flight 訂單無法繼續推進。**Scrape 當下資料庫查詢失敗時,gauge 會送 `0`(而非沉默)** — 避免 Prometheus 的 5 分鐘 staleness 窗口讓 `OutboxPendingBacklog` 在資料庫離線期間用上一次 scrape 的舊值繼續觸發。資料庫離線時的真相訊號是 `outbox_pending_collector_errors_total`(上升中的 counter)加配對告警 `OutboxPendingCollectorDown`;gauge 讀到 0 代表「我們看不到」而不是「積壓已排空」。配對告警:`OutboxPendingBacklog`(warning)+ `OutboxPendingCollectorDown`(critical)。 |
| **Redis 與資料庫之間的庫存漂移(PR-D)** | `inventory_drift_detected_total{direction}` — counter,標籤為 `cache_missing` / `cache_high` / `cache_low_excess`。`recon` 程序中的漂移偵測器每次 sweep 發現某個事件的 Redis 數量跟 `events.available_tickets` 差距超過 `INVENTORY_DRIFT_ABSOLUTE_TOLERANCE`(預設 100)就 +1。<br>• `cache_missing` = Redis 回 0 / key 不存在但資料庫有庫存 → Redis 重置後 rehydrate 沒跑。<br>• `cache_high` = Redis > 資料庫 → saga 補償 desync,或手動 SetInventory 超過 reset 基線。<br>• `cache_low_excess` = Redis < 資料庫且超過容忍值 → worker 沒成功提交,或 recon 強制失敗洩漏庫存。<br>配套 gauge:`inventory_drifted_events_count`(point-in-time,每次 sweep 重設)、`inventory_drift_sweep_duration_seconds`(histogram)、`inventory_drift_list_events_errors_total` / `inventory_drift_cache_read_errors_total`(sweep 內部失敗 counter)。配對告警:`InventoryDriftDetected`(warning,`for: 5m`)、`InventoryDriftListEventsErrors`(critical — 資料庫端瞎掉)、`InventoryDriftCacheReadErrors`(warning — 部分可見)。Cache-truth 路線圖的最後一塊;詳見 `docs/architectural_backlog.md` § Cache-truth architecture。 |
| **Sweeper goroutine panic(靜默失敗 canary)** | `sweep_goroutine_panics_total{sweeper}` — counter,以 sweeper 區分(`recon` / `inventory_drift` / `once_recon` / `once_drift`)。Sweep goroutine panic 被 loop 的 `defer recover()` 救回時 +1。**健康的 production 應該永遠是 0。** 沒有這個 counter,被 recover 救回的 panic 對 operator 是隱形的(process 還活著、/metrics 還在送上一次的 gauge 值、`up{}` 還是 1 → TargetDown 不會觸發)。配對告警 `SweepGoroutinePanic` 第一次發生就觸發(無 soak、critical)。 |
| 同一個 Idempotency-Key 被重送時的處理結果(N4) | `idempotency_replays_total{outcome}` — 每次 client 帶**重複**的 Idempotency-Key 來時,server 怎麼處理。三種結果:<br>• `match` = 同 key + 同 body → 我們直接回傳之前快取的回應(這是冪等正常運作)<br>• `mismatch` = 同 key + **不同** body → 我們回 409 Conflict(client 程式有問題:把同一把 key 用在意義不一樣的請求上)<br>• `legacy_match` = 在 N4 上線之前就已經寫進快取的舊條目(沒帶 fingerprint),仍然回傳但順便補寫 fingerprint。**部署後 24 小時內應該降到 0**(舊快取會自然過期);如果一直 > 0,表示有東西還在寫舊格式 — 要查為什麼。 |
| Idempotency 查 Redis 失敗的次數(N4) | `idempotency_cache_get_errors_total` — idempotency 在查 Redis 時失敗了幾次(Redis 連線斷、回傳資料壞掉)。**這條值得 page on-call**。意思:當這條持續 > 0,booking 端點還在收單,但**冪等保護是關掉的** — 同一個請求重送會被處理兩次(僅剩 DB 的 UNIQUE 限制在最底層擋)。<br>**為什麼還繼續服務不直接拒絕?** 因為 Redis 一斷就拒絕所有訂票請求 = 整個端點掛掉,「冪等保護暫時失效」比「服務整個掛」好;這個 counter 就是讓 on-call 知道目前處在那個狀態。<br>告警設定:`rate(idempotency_cache_get_errors_total[5m]) > 0 for 1m` → page。 |
| 卡在 Failed 狀態還沒被補償的訂單數(A5) | `saga_stuck_failed_orders` — gauge,每次 saga watchdog 跑 sweep 時更新。值代表「在 Failed 狀態超過 `SAGA_STUCK_THRESHOLD`(預設 60s)的訂單數」。**單一時刻 > 0 沒問題**(下一次 sweep watchdog 會去重新觸發 compensator)。**持續 > 0 達 10 分鐘才是問題** — 表示 compensator 一直失敗,典型原因:Redis revert 卡住、DB lock 競爭、未被 map 的 compensator 錯誤。Watchdog 預設 60s sweep 一次,10 分鐘 = ~10 次重試後才 page,給足時間自然清空。 |
| Saga watchdog 處理結果(A5) | `saga_watchdog_resolved_total{outcome}` — counter,記每次 watchdog 重新觸發 compensator 的結果。**六個 outcome**,每個對應不同的 runbook,讓 on-call 一開始就找對子系統:<br>• `compensated` = watchdog 成功重新觸發 compensator(Failed → Compensated)<br>• `already_compensated` = 在 FindStuckFailed 跟重新觸發中間,saga consumer 自己跑完了(良性,不需處理)<br>• `max_age_exceeded` = 訂單超過 `SAGA_MAX_FAILED_AGE`(預設 24h);watchdog 只記錄 + 告警,**不會自動轉狀態**(沒驗證庫存是否已回復就 MarkCompensated 不安全)— 需要人工調查<br>• `getbyid_error` = `orderRepo.GetByID` 在到 compensator 之前就失敗了。Operator 先查 **資料庫** 健康度,**不是** Redis 或 compensator 程式碼<br>• `marshal_error` = 合成 OrderFailedEvent 時 `json.Marshal` 失敗。今天對固定形狀的 struct 是理論值;單獨 label 是為了讓未來欄位 regression 可被觀測<br>• `compensator_error` = `compensator.HandleOrderFailed` 回傳錯誤。Operator 查 **Redis revert path + DB lock 競爭**。下個 sweep 會重試 |
| Saga watchdog 的 DB 查詢失敗(A5) | `saga_watchdog_find_stuck_errors_total` — `FindStuckFailed` SQL 查詢失敗(DB 斷線、000011 migration 沒跑、query timeout)。**Critical 等級的 page**:這條 > 0 時 watchdog 完全看不到任何 stuck 訂單 — `saga_stuck_failed_orders` gauge 會卡在舊值看起來「健康」但其實是瞎的。對應告警在 2 分鐘內觸發。 |
| **Saga compensator — Kafka 驅動熱路徑處理結果(D12.4)** | `saga_compensator_events_processed_total{outcome}` — counter,記 saga consumer 透過 `Compensator.HandleOrderFailed` 處理的每筆 `order.failed` event。跟 `saga_watchdog_resolved_total` 不同 — 那是 DB 端的 sweeper;這條是 Kafka 消費者。**13 個 outcome**(每個對應不同 runbook;4 個 UoW 內部各步驟 DB 失敗在下方併成一個項次方便閱讀):<br>• `compensated` = 完全成功(UoW + Redis revert)<br>• `already_compensated` = Kafka at-least-once 重投碰到已經 Compensated 的訂單(良性)<br>• `already_compensated_redis_error` = 同上但冪等的 Redis 重新嘗試失敗;SETNX 守門通常讓這個是良性的<br>• `path_c_skipped` = `resolveTicketTypeID` 回傳 uuid.Nil(rolling-upgrade 期間有多 ticket_type 的舊事件)。UoW + Redis 都跳過。**> 0 即告警** — 庫存沒被回復<br>• `unmarshal_error` = JSON payload 壞掉(consumer 另外送 DLQ)<br>• `getbyid_error` / `list_ticket_type_error` / `incrementticket_error` / `markcompensated_error` = UoW 內部 4 個獨立步驟的 DB 失敗(各自獨立的 counter series)<br>• `redis_revert_error` = `RevertInventory` 在新鮮的補償流程中失敗。PG 已 commit 但 Redis qty 漏了。**Critical — 該 page**<br>• `context_error` = UoW 把 `context.Canceled` / `context.DeadlineExceeded` 傳出來。通常是 shutdown 雜訊<br>• `uow_infra_error` = UoW 回傳的不是 stepError(tx-begin 失敗、連線池耗盡)。是 DB / pool 健康度議題<br>• `unknown` = 延遲執行的 sentinel — 只在某條程式碼路徑忘了呼叫 `record()` 時觸發。**> 0 即告警** — 程式碼 regression |
| **Saga compensator — 端到端 loop duration(D12.4)** | `saga_compensation_loop_duration_seconds` — histogram,衡量 `events_outbox.created_at → MarkCompensated commit`(透過 `kafka.Message.Time` 串接)。包含 outbox poll 延遲(預設 500ms)、Kafka round-trip、consumer FetchMessage、compensator UoW、Redis revert。bucket 上限到 5 分鐘(`{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}`),讓事故規模的延遲也看得見。**只在 `compensated` 成功路徑記錄**;`already_compensated` 跟 `path_c_skipped` 是 no-op,記錄它們會把 p50/p99 拉到地板,蓋掉真實 degradation。 |
| **Saga compensator — consumer 落後時間 gauge(D12.4)** | `saga_compensation_consumer_lag_seconds` — 最近一筆處理過的 `order.failed` 訊息對應的 `time.Since(msg.Time)`。**這是效能指標,不是 liveness** — 如果 consumer 當掉,gauge 會卡在最後一個值,直到 Prometheus 把目標標記為 down(`up == 0` 約 `scrape_interval × 3` 之後)。Idle-reset goroutine(5s 一次 tick,30s 門檻)會在系統靜默時把 gauge 歸零,讓沒事在跑的 consumer 不會誤觸發告警。時鐘偏移造成的負值會被 clamp 到 0。多副本部署需用 `max by (instance)` 聚合。對應的 `SagaConsumerLagHigh` 告警**必須 gate 在 `up == 1`**,避免 consumer 當掉時誤觸發。 |
| Page funnel | `page_views_total{page}` |
| **D5 付款 webhook — 處理結果總計** | `payment_webhook_received_total{result}` — 每筆走進 application service 的 event 都會 +1。`result` ∈ {`succeeded`, `failed`, `unsupported`, `unexpected_status`, `persist_failed`, `malformed`}。熱路徑流量看這條;告警讀更具體的 counter(下方)。 |
| **D5 付款 webhook — 簽章驗證失敗(可 page)** | `payment_webhook_signature_invalid_total{reason}` — 驗章端拒絕計數。`reason` ∈ {`missing`, `malformed`, `skew_exceeded`, `mismatch`, `config_error`}。任何穩定 > 0 速率會觸發 `PaymentWebhookSignatureFailing`(warning,5m soak)。`mismatch` 比例高通常是 secret rotation 還沒做完。 |
| **D5 付款 webhook — 找不到 intent(critical)** | `payment_webhook_unknown_intent_total{reason}` — `metadata.order_id` 跟 `payment_intent_id` 都查不到 order 時觸發。`reason="not_found"` 救援的是 `SetPaymentIntentID` race orphan(critical,該 page:每筆都可能是「客戶付了但沒對應 order」)。`reason="cross_env_livemode"` 是良性(200 ACK,livemode 不符)。 |
| **D5 付款 webhook — 重複投遞** | `payment_webhook_duplicate_total{prior_status}` — 進來的 event 撞到已經是 terminal 的 row(provider 重投),或並發 webhook 競爭走 `ErrInvalidTransition` 重讀路徑時觸發。`prior_status` ∈ {`paid`, `payment_failed`, `expired`, `compensated`}。健康時以 `paid`/`payment_failed` 為主。 |
| **D5 付款 webhook — intent 不一致(單筆即 page)** | `payment_webhook_intent_mismatch_total` — metadata 解析到的 order 上,持久化的 `payment_intent_id` 跟 webhook envelope 的 `object.id` 不同。可能是被偽造、測試 fixture 漏到 prod、或 provider 真有 bug。**單筆即 page**:告警是 `increase(...[5m]) > 0` 配 `for: 0m`。 |
| **D5 付款 webhook — late success(單筆即 page)** | `payment_webhook_late_success_total{detected_at}` — `succeeded` webhook 在 reservation TTL 過期之後才到。錢在 provider 端已經動了但我們的 hold 已死;handler 把 order 走到 `expired` + emit `order.failed` 給 saga 補償,**但人工必須去 provider 端開退款單**。每一筆都是一張退款工單。`detected_at` ∈ {`service_check`, `sql_predicate`, `post_terminal`}(第三個 label 表示 webhook 到達時 order 已經是 terminal 狀態 — D6 已 expire 或 saga 已 compensate)。 |
| **D5 付款 webhook — 不支援的 event type** | `payment_webhook_unsupported_type_total{event_type}` — 計數那些 dispatcher 不處理的 `Type`(目前:除 `payment_intent.succeeded` / `payment_intent.payment_failed` 以外都算)。當這個值上升時,做為「該不該開始支援這個 type」的指引。 |
| **D6 expiry sweeper — 每筆 row 結果** | `expiry_sweep_resolved_total{outcome}` — sweeper 每處理一筆 row 都會 +1。`outcome` ∈ {`expired`, `expired_overaged`, `already_terminal`, `getbyid_error`, `marshal_error`, `outbox_error`, `transition_error`}。`expired_overaged` 是「超過 `EXPIRY_MAX_AGE` 但仍然完成 expire」的標記訊號(round-1 P1 契約:D6 永遠 expire,MaxAge 純為觀測)。已 pre-warm。 |
| **D6 expiry sweeper — backlog 訊號(可 page)** | `expiry_oldest_overdue_age_seconds` — gauge,每次 sweep 設成最舊一筆仍 overdue 的 `awaiting_payment` row 的年齡(NOW − reserved_until)。配合 `expiry_backlog_after_sweep`(本次批次處理完後仍 past cutoff 的 row 數,steady state 為 0)。健康時最舊年齡保持在 SweepInterval + ExpiryGracePeriod + 小常數 ≈ 35s 內;持續超過 5min 觸發 `ExpiryOldestOverdueAge`。**注意:**當 `expiry_find_expired_errors_total` 失敗時,gauge 會被「保留在 last-known-good」(round-3 F2 契約)— `ExpiryFindErrors` 期間看到的數值可能是事故前的舊值。 |
| **D6 expiry sweeper — find / count 失敗(資料庫盲點,critical)** | `expiry_find_expired_errors_total` — sweeper 無法掃表 OR 無法做 post-sweep backlog count。持續 > 0 觸發 `ExpiryFindErrors`(critical)。把兩種查詢失敗合在同一個 counter,因為 operator 處理方式相同(查資料庫連線 / 索引存在 / pool 設定)。Round-2 F3 + round-3 F2 設計。 |
| **D6 expiry sweeper — MaxAge 觀測(單筆即 page)** | `expiry_max_age_total` — 每次 outcome 是 `expired_overaged` 時也會 +1。獨立 counter(**不是** resolved-vector 的 label),讓告警能在很細的切面觸發,不用篩 busy 的 resolved vector。純觀測 — 觸發時 row 已被 expire,告警只是要 operator 去查「為什麼 row 在 sweeper 處理前過了 `EXPIRY_MAX_AGE`(預設 24h)」。 |
| **D6 expiry sweeper — per-row vs full-sweep duration** | `expiry_resolve_duration_seconds`(每筆 row)+ `expiry_sweep_duration_seconds`(整個 Sweep() 的 wall time,saga 沒有對應指標)。round-3 F4 拆開語意:儀表板分開看「sweep 是否落後?」(full-sweep > SweepInterval)跟「個別 row 是否慢?」(per-row p95)。再加 `expiry_resolve_age_seconds` — 轉換時的 row 年齡 SLO histogram。 |
| **D4.2 Stripe SDK 介面卡 — 每次呼叫結果** | `stripe_api_calls_total{op,outcome}` — StripeGateway 介面卡 ([internal/infrastructure/payment/stripe_gateway.go](../internal/infrastructure/payment/stripe_gateway.go)) 每次呼叫 `api.stripe.com` 都會 +1。`op` ∈ {`create_payment_intent`, `get_status`}(操作名,不帶 Stripe 前綴或 `/v1` 路徑);`outcome` 是分類後的結果 — `success` / `declined` / `transient` / `misconfigured` / `invalid` — 對應 `domain.ErrPayment*` sentinel 分類。啟動時透過 [`metrics_init.go`](../internal/infrastructure/observability/metrics_init.go) 預熱所有 label,讓 PromQL 警示從第 0 秒就可以求值。Operator 依 outcome 分流:`transient` = 可重試(429 / 5xx / 網路);`misconfigured` = page 等級(401 / 403 — 可能是 API key 錯誤或已停用);`declined` = 卡片端錯誤(對訂票 client 變成 422);`invalid` = 400 / idempotency 偏差(不可重試,屬於應用 bug)。 |
| **D4.2 Stripe SDK 介面卡 — 每次呼叫耗時** | `stripe_api_duration_seconds{op}` — 單次 Stripe API call wall-clock 耗時 histogram。Bucket:`ExponentialBuckets(0.01, 2, 14)` 涵蓋 10ms → 81.92s。對 p50(localhost 等效約 30-50ms)、p99(Stripe 公布 SLO 約 89ms)以及超時長尾(預設 30s,介面卡上限 5min)都有解析度。stripe-go 的 MaxNetworkRetries 迴圈裡每次重試都會獨立紀錄一筆,所以 histogram 反映的是每次嘗試的延遲,而不是累計預算。搭配 `stripe_api_calls_total` 可算出依 outcome 分層的延遲:`histogram_quantile(0.99, sum by (le, op) (rate(stripe_api_duration_seconds_bucket[5m])))`。 |

### 為什麼 label 在啟動時就有值

程式碼會在啟動時預先註冊預期的 label 組合,避免儀表板對「目前還沒發生過事件」的指標顯示「no data」。新啟動的 stack 上會看到例如 `bookings_total{status="success"} 0` 這樣的初始列。這是有意的 — 見 [observability/metrics.go](../internal/infrastructure/observability/metrics.go) 的開頭。

---

## 3. Prometheus UI 工作流

開 http://localhost:9090。

**會用到的三個 nav:**

| Nav | 用途 |
| :-- | :-- |
| **Graph** | 打 PromQL,按 Execute,切到 Graph 分頁。是「X 有沒有在發生」最快的介面。 |
| **Status → Targets** | 確認 app process 有被 scrape 到(`app:8080/metrics` 旁邊應該是 `UP`)。這裡紅 = scrape 失敗 = 其他所有指標都是過期值。 |
| **Status → Alerts** | 全部已定義的告警,以及目前狀態(Inactive / Pending / Firing)。 |

**值得收藏的查詢:**

```promql
# 訂票漏斗 — 成功 vs 售完 vs 重複 vs 錯誤
sum by (status) (rate(bookings_total[1m]))

# Worker 吞吐
sum by (status) (rate(worker_orders_total[1m]))

# 各路徑的 p99 latency
histogram_quantile(0.99,
  sum by (le, path) (rate(http_request_duration_seconds_bucket[5m]))
)

# DB pool 飽和度 — 持續 > 0 表示 worker 在排隊等連線
db_pool_wait_count

# Stream 積壓 — 健康的 worker 應該把它排空到 ~0
redis_stream_length{stream="orders:stream"}

# 卡在 Charging 的對帳器 — 持續 > 0 = gateway 退化或對帳器跟不上
recon_stuck_charging_orders
```

---

## 4. Grafana 工作流

開 http://localhost:3000,登入 `admin` / `admin`。**Dashboards → Browse → Booking Monitor Dashboard**。

預先配置的 panel 在 [deploy/grafana/provisioning/dashboards/dashboard.json](../deploy/grafana/provisioning/dashboards/dashboard.json)。Provisioning 在 UI 上是**唯讀的** — 你在瀏覽器裡的修改在 `docker compose down` 之後不會保留。要做永久的修改,改 JSON 檔再重啟 Grafana。

Panel 以可摺疊的 row 分組。最上方放「黃金訊號」,reliability / 基礎設施的 row 在下方。

**黃金訊號(dashboard 最上方):**
- Request Rate (RPS) — 依 method/path/status 切分
- Global Request Latency (p99 / p95 / p50)
- Conversion Rate (%) — `bookings_total{status="success"}` / `page_views_total{page="event_detail"}`
- Saturation — Goroutines
- Saturation — Memory Alloc Bytes

**Row:Reliability — Recon(A4 charging 兩階段意圖紀錄)**
- Recon resolved by outcome (rate, 5m) — `charged` / `declined` / `not_found` / `unknown` / `max_age_exceeded` / `transition_lost`
- Recon stuck-charging 計量 — `recon_stuck_charging_orders`(時點值)
- Recon resolve duration p95/p50 — `recon_resolve_duration_seconds_bucket`
- Recon 錯誤率 — find-stuck(資料庫)/ gateway 探測 / mark(資料庫+outbox)

**Row:Reliability — Saga Watchdog(A5)**
- Saga watchdog resolved by outcome (rate, 5m) — `compensated` / `already_compensated` / `max_age_exceeded` / `getbyid_error` / `marshal_error` / `compensator_error`
- Saga stuck-failed 計量 — `saga_stuck_failed_orders`
- Saga watchdog resolve duration p95/p50
- Saga watchdog find-stuck 錯誤率 + saga poison 訊息數

**Row:Dead Letter Queue 活動**
- Kafka DLQ 訊息依 topic + reason — `dlq_messages_total`
- Redis DLQ routed by reason — `redis_dlq_routed_total`
- Kafka consumer retry rate — `kafka_consumer_retry_total`(silent-retry 表面)

**Row:資料庫 — 連線池(USE)+ 正確性訊號**
- PG pool:in-use vs idle — `pg_pool_in_use` / `pg_pool_idle`
- PG pool 等待頻率 + 等待時間 + rollback 失敗 — `pg_pool_wait_count_total` / `pg_pool_wait_duration_seconds_total` / `db_rollback_failures_total`

**Row:Cache — idempotency(N4)**
- Idempotency cache 命中率 (%) — `cache_hits_total{cache="idempotency"}` /(hits + misses)
- Idempotency cache GET 錯誤 — `idempotency_cache_get_errors_total`(會 page:rate >0 持續 1m 表示 idempotency 保護已停擺)
- Idempotency replay 結果 — `match` / `mismatch` / `legacy_match`

**Row:Redis — stream / DLQ 基礎設施失敗**
- Stream/DLQ 失敗率 — `redis_xack_failures_total` / `redis_xadd_failures_total{stream}` / `redis_revert_failures_total`
- Stream collector 錯誤依 stream + operation — `redis_stream_collector_errors_total`

**Row:Meta — scrape 健康度(TargetDown)**
- Scrape target 上/下線 — `up`(每個 `{job, instance}`,1 = 健康、0 = 掛掉)。與 `TargetDown` 告警搭配;持續為 0 代表依賴該 job 指標的所有 rate 告警都已經無聲沉默。

**第二個預先配置的 dashboard:Redis Exporter**

[deploy/grafana/provisioning/dashboards/redis-exporter.json](../deploy/grafana/provisioning/dashboards/redis-exporter.json) — Grafana 社群 dashboard `#763`(oliver006 自己的參考 dashboard,內嵌進 repo 是為了讓整個 stack 離線也能跑)。位置在 **Dashboards → Browse → Redis Exporter (oliver006/redis_exporter)**。

Panel 涵蓋 §2 Redis 伺服器端子表格列出的指標家族:每個指令的 rate + duration、CPU 切分(sys vs user)、記憶體 + fragmentation、connected/blocked clients、命中率、expired/evicted keys、網路 I/O。triage「Redis 熱路徑本身是不是瓶頸?」時跟主要的 Booking Monitor dashboard 搭配著看。兩個 dashboard 刻意分開:主 dashboard 是**應用層**視角(每個應用資源的 RED + USE),這個是**基礎設施**視角(Redis 內部計數器)。

**快速加一個新 panel(暫時的 — 只用來探索):**
1. 點 **+ → Create dashboard → Add visualization**。
2. 選 **Prometheus** 資料來源。
3. 從 §3 貼一個 PromQL,並調整視覺化類型。
4. 如果 panel 值得保留,把 panel JSON 複製出來合進 `dashboard.json`,這樣下次 `down/up` 才不會掉。

---

## 5. 告警

告警定義在 [deploy/prometheus/alerts.yml](../deploy/prometheus/alerts.yml)。狀態在 Prometheus UI → **Alerts**,以及 Alertmanager UI(http://localhost:9093,負責 silence / inhibit / 通知紀錄)。

**Alertmanager 接線(CP6,2026-05-02 更新預設投遞)。** Prometheus 把 firing 的告警推到 Alertmanager(設定檔:[deploy/alertmanager/alertmanager.yml](../deploy/alertmanager/alertmanager.yml))。Alertmanager 負責去重、依 `alertname + severity` 分組、依 severity 設定不同的 repeat 節奏(critical:30 m;warning:4 h;info:24 h)、以及 inhibition(例如 `RedisStreamCollectorDown` 會壓制所有其他 stream-backlog 告警,因為計量本來就已經是 stale)。

**預設投遞(自 2026-05-02 起):webhook-logger sidecar。** 告警會被 POST 到 `booking_alert_logger`(一個 `mendhak/http-https-echo` container),它會把 payload 印到 stdout。操作員透過 `docker logs booking_alert_logger -f` 就能看到 firing 的告警。這取代了原本的 `null` receiver 預設 — senior-review checkpoint 指出舊預設讓告警根本沒離開 Alertmanager。Slack 投遞仍然是 opt-in:把 `deploy/alertmanager/alertmanager.slack.yml.example` 複製成 `alertmanager.yml`、把 Incoming Webhook URL 貼進 `api_url`,然後 `docker compose restart alertmanager`。

**Runbook annotation(CP5)。** 每個告警都帶一條 `runbook_url` annotation,指向 [docs/runbooks/README.md](runbooks/README.md) 內的某個 section。Alertmanager 會透過 `alertmanager.yml` 中的 template 把該 URL 渲染進 Slack 通知。操作員的工作流:告警觸發 → 通知到達 → 點 runbook → 同一份文件內看到對應的 dashboard panel 與具體的處置步驟。

目前的告警目錄:

| 告警 | severity | 症狀 |
| :-- | :-- | :-- |
| `HighErrorRate` | critical | 5xx 比率 > 5% 持續 5m |
| `HighLatency` | warning | p99 > 2s 持續 2m+(5m rate 視窗 — 抗抖動;Phase 2 cleanup 之前是 1m/1m) |
| `InventorySoldOut` | info | 有訂票嘗試回 sold_out |
| `OrdersStreamBacklogYellow` | info | Stream 長度 > 10K 持續 2m |
| `OrdersStreamBacklogOrange` | warning | Stream 長度 > 50K 持續 2m |
| `OrdersStreamBacklogRed` | critical | Stream 長度 > 200K 持續 1m |
| `OrdersStreamConsumerLag` | warning | 最舊 pending 條目 > 60s 持續 2m |
| `OrdersDLQNonEmpty` | warning | DLQ 有未檢視條目持續 5m |
| `RedisStreamCollectorDown` | critical | Streams scrape 錯誤 → 其他 stream 告警會變沉默 |
| `ReconStuckCharging` | warning | `recon_stuck_charging_orders` > 0 持續 5m |
| `ReconFindStuckErrors` | critical | 對帳器 sweep 查詢失敗中 |
| `ReconGatewayErrors` | warning | 對帳器 gateway 錯誤率升高 |
| `ReconMaxAgeExceeded` | critical | 對帳器強制把訂單標 failed — 需人工檢視 |
| `ReconMarkErrors` | warning | `recon_mark_errors_total` rate > 0 持續 5m — 對帳器在 resolve 階段做 DB transition 失敗 |
| `SagaStuckFailedOrders` | warning | `saga_stuck_failed_orders > 0 for 10m` — compensator 一直在失敗 |
| `SagaCompensatorErrors` | warning | `rate(saga_watchdog_resolved_total{outcome="compensator_error"}[5m]) > 0 for 2m` — 快速路徑配對告警;在 gauge 告警的 10m 視窗到之前就抓到 100% 失敗的 compensator |
| `SagaWatchdogFindStuckErrors` | critical | Watchdog sweep 查詢失敗 — gauge 看起來健康但其實是瞎的 |
| `SagaMaxFailedAgeExceeded` | critical | 卡在失敗終態超過 24h — 需要人工調查(watchdog **不會**自動轉狀態)。D2(Pattern A)後 watchdog 涵蓋 `failed`(舊 A4)+ `expired`(Pattern A 預訂 TTL)+ `payment_failed`(Pattern A webhook 失敗);三者皆透過同一個 compensator 走到 Compensated。 |
| `SagaCompensatorErrorRate`(D12.4) | warning | `saga_compensator_events_processed_total` 的非成功 outcome 比例持續 5 分鐘 > 5%。`compensated`、`already_compensated`、以及 `already_compensated_redis_error` 都當成功計算(最後一個是 SETNX 守門過的冪等重新嘗試小波動 — Redis 狀態其實已被先前那筆投遞補正過,把它列在錯誤分子裡會在 Kafka 重投高峰期誤觸發)。其他(path_c_skipped、各 *_error label、context_error、uow_infra_error、unknown)才算錯誤。誤觸發提醒:`path_c_skipped` 在 rolling upgrade 後 ~1 小時內可能短暫佔比偏高(pre-v3 多 ticket_type 事件流出);各 outcome 的 triage 見 runbook。 |
| `SagaConsumerLagHigh`(D12.4) | warning | `saga_compensation_consumer_lag_seconds > 30 for 2m` 且 `up{job="booking-service"} == 1`(gate 在 up==1,避免 consumer 當掉時 gauge 卡住誤觸發)。lag 涵蓋 outbox poll + Kafka + 處理時間。持續觸發代表 Kafka 累積 OR compensator 變慢。**前向相容提醒**:PR-D12.5 加入 per-stage scrape job 後可能把 `booking-service` 改名為 `d12-stage4`,這個 gate 必須一起更新。**k8s 注意**:`and on(instance)` join 在 gauge 與 `up` 的 `instance` label 不一致時會靜默回傳空集合;若所有 pod 共用同一個 job,改用 `on(job)`。 |
| `SagaCompensatorClassifierDrift`(D12.4) | warning | `increase(saga_compensator_events_processed_total{outcome="unknown"}[5m]) > 0` — 延遲執行的 sentinel「unknown」label 一旦 +1 就觸發。永遠是程式 regression:某次重構新增了 return path 但沒呼叫 `record()`。從最近的 deploy diff 找到新增的 return,補上對應的 `record(...)`,出版本。 |
| `SagaCompensatorRedisInventoryLeak`(D12.4) | critical | `increase(saga_compensator_events_processed_total{outcome="redis_revert_error"}[5m]) > 0` — 新鮮的補償流程中 Redis revert 失敗。PG 已被 MarkCompensated 但 Redis qty **沒**回復 — 永久庫存漏失,需人工 revert。**跟 `already_compensated_redis_error` 不同**(那是冪等的重新嘗試出錯;SETNX 守門通常代表 Redis 狀態其實已經由前一筆投遞補正,屬於良性)。 |
| `KafkaConsumerStuck` | warning | Consumer rebalance retry — 下游依賴退化中 |
| `IdempotencyCacheGetErrors` | warning | `idempotency_cache_get_errors_total` rate > 0 持續 1m — 重複扣款防護被暫停了 |
| `DBRollbackFailures` | warning | `db_rollback_failures_total` rate > 0 持續 5m — UoW Rollback 失敗(driver / connection-state bug) |
| `RedisXAckFailures` | warning | `redis_xack_failures_total` rate > 0 持續 5m — PEL 會無限增長;rebalance 後 consumer 會重做工作 |
| `RedisRevertFailures` | warning | `redis_revert_failures_total` rate > 0 持續 5m — saga 補償時 revert.lua 失敗,Redis 庫存沒被還原 |
| `RedisXAddFailures` | warning | `redis_xadd_failures_total` rate > 0 持續 5m — 訂票 hot path 間歇性無法 enqueue |
| `ConsumerGroupRecreated` | critical | `increase(consumer_group_recreated_total[5m]) > 0` — worker 遇到 NOGROUP 並透過 `XGROUP CREATE ... $` 自癒。可用性保住了,但**消費者群組消失到重建之間進入 stream 的訊息會被靜默丟棄**。第一次發生就觸發(無 soak);`[5m]` 視窗跟 Prometheus 預設 1m 的 eval 節奏有充分重疊,單一尖峰不會被漏掉。要查:operations 是否跑了 FLUSHALL?Redis 是否在 AOF 關閉的情況下 crash?交叉比對 `bookings_total` 跟 DB orders 筆數找漂移。詳見 `docs/architectural_backlog.md` § Cache-truth architecture。 |
| `OutboxPendingBacklog` | warning | `outbox_pending_count > 100 for 5m` — OutboxRelay 卡住了。D7 後(2026-05-08)outbox 只剩 `order.failed` rows,客戶仍可下單 + 付款(`/pay` 與 webhook 狀態轉換不受影響)— 卡住的是 D5 webhook(`payment_failed`)+ D6 expiry sweeper(`expired`)+ recon force-fail 對 in-process saga consumer 的補償扇出。Failed / expired 訂單會停在 `failed` / `expired` 而不會推進到 `compensated`;Redis 庫存不會被 revert,跟 DB 漂移。診斷:`pg_locks` 是否有 zombie advisory lock 1001、relay container 是否還活著、broker 連得上嗎。 |
| `OutboxPendingCollectorDown` | critical | `rate(outbox_pending_collector_errors_total[5m]) > 0 for 2m` — 每次 scrape 的 COUNT(events_outbox WHERE processed_at IS NULL) 查詢在失敗。觸發期間 `outbox_pending_count` 是空的或過期值,`OutboxPendingBacklog` 也無法觸發。診斷:資料庫離線、缺 000007 partial index、查詢逾時。流程跟 `RedisStreamCollectorDown` 一樣。 |
| `InventoryDriftDetected` | warning | `increase(inventory_drift_detected_total[5m]) > 0 for 5m` — 漂移偵測器發現某個事件的 Redis 數量跟 `events.available_tickets` 不一致。三種 direction 標籤對應不同的處理路徑:`cache_missing` → rehydrate 沒跑;`cache_high` → saga / 手動 desync;`cache_low_excess` → worker 沒成功提交,或 recon 強制失敗洩漏庫存。`for: 5m` 用來區分「短暫的 in-flight 起伏」跟「持續性的資料損壞」。Cache-truth 4-PR 計畫的最後一塊(PR-D)。詳見 `docs/architectural_backlog.md` § Cache-truth architecture。 |
| `InventoryDriftListEventsErrors` | critical | `rate(inventory_drift_list_events_errors_total[5m]) > 0 for 2m` — 漂移偵測器每次 sweep 的 `ListAvailable` 查詢在失敗。觸發期間偵測器在資料庫端瞎掉:每次 sweep 都中斷、gauge 保持為 0、`InventoryDriftDetected` 也無法觸發(因為沒東西可偵測)。診斷:資料庫離線、connection pool 爆掉、recon container 端的查詢逾時。形狀跟 `ReconFindStuckErrors` 與 `SagaWatchdogFindStuckErrors` 一致。 |
| `InventoryDriftCacheReadErrors` | warning | `rate(inventory_drift_cache_read_errors_total[5m]) > 0 for 5m` — 漂移 sweep 期間的 per-event Redis GET 失敗。Sweep 繼續但只有部分可見性(log 中 `events_skipped` > 0)。診斷:Redis 連線池爆掉(`redis_client_pool_timeouts_total`)、Redis CPU 飽和、慢 key(`redis_slowlog_length`)。`for: 5m` 寬鬆是因為 per-event Redis 短暫故障是合理的背景雜訊。 |
| `SweepGoroutinePanic` | critical | `increase(sweep_goroutine_panics_total[5m]) > 0 for 0s` — sweeper goroutine(recon / inventory_drift / once_recon / once_drift)panic 被 loop 的 `defer recover()` 救回了。Process 還活著但有確定性 bug 浮上來。沒有這個告警的話,救回來的 panic 是隱形的(`up{}` 還是 1、TargetDown 不會觸發)。把 recon container log 用 `recovered from panic` 過濾,看 panic value 和 stack trace,再開 bug。 |
| `TargetDown` | critical | `up == 0` 對任一個 (job, instance) 持續 2m+ — meta 告警;依該 job 指標的所有 rate 告警在 scrape 恢復前都已經無聲沉默 |
| `RedisExporterCannotReachRedis` | critical | `redis_up{job="redis"} == 0` 持續 1m — exporter HTTP listener 還活著(`TargetDown` 不會觸發)但它連不到 Redis 本身。典型原因:REDIS_PASSWORD 錯誤/輪換、Redis container 掛了、docker 網路斷裂。Dashboard 上每一個 Redis-server 指標在這個解除前都會是過期值。 |
| `PaymentWebhookSignatureFailing` | warning | `sum(rate(payment_webhook_signature_invalid_total[5m])) > 0.1 for 5m` — D5 驗章端在拒收 webhook。看 `reason` label 分布:近期剛輪換過密鑰、`mismatch` 比例高 = 部署還沒完成;沒輪換、`mismatch` 還高 = 可能是事件;`skew_exceeded` = provider 端或 pod 時鐘問題;`missing` / `malformed` = 被探測或被誤路。 |
| `PaymentWebhookUnknownIntentSurging` | critical | `sum(rate(payment_webhook_unknown_intent_total{reason="not_found"}[5m])) > 0.05 for 5m` — D5 webhook 用 `metadata.order_id` 或 `payment_intent_id` 都無法找到對應 order。最可能的原因:`/pay` 的 `SetPaymentIntentID` race(gateway 已建立 intent 但資料庫寫入失敗)。每一筆都可能是「客戶付了但沒對應 order」;每筆都得人工調帳。 |
| `PaymentWebhookLateSuccessAfterExpiry` | critical | `increase(payment_webhook_late_success_total[5m]) > 0` — D5 單筆即 page。`succeeded` webhook 在 `reserved_until` 過期之後才到:錢在 provider 端動了但我們的 hold 已死。Handler 把 order 走到 `expired` + emit `order.failed` 給 saga 補償;**人工必須去 provider 端開退款**。`detected_at` label(`service_check` vs `sql_predicate`)可以分辨是 `BOOKING_RESERVATION_WINDOW` 設太短(service_check 為主)還是 ms 級的窄 race(sql_predicate 為主)。 |
| `PaymentWebhookIntentMismatch` | critical | `increase(payment_webhook_intent_mismatch_total[5m]) > 0` — D5 單筆即 page。`metadata.order_id` 解析到的 order,持久化的 `payment_intent_id` 跟 webhook 上的 `object.id` 不同。可能是被偽造、測試 fixture 漏到 prod、或 provider 真有 bug。Handler 已拒絕翻單 — 必須由人工調查後才能自動處理。當作潛在資安事件處理。 |
| `ExpiryOldestOverdueAge` | warning | `expiry_oldest_overdue_age_seconds > 300 for 5m` — D6 sweeper 落後。健康時最舊年齡保持在 SweepInterval + grace + 小常數 ≈ 35s 內;持續 5min 表示 row 一直 eligible 但沒被處理(BatchSize 太小 OR per-row resolve 在錯)。先 triage `ExpiryProcessingErrors`;若是純吞吐量問題,提高 `EXPIRY_BATCH_SIZE`。 |
| `ExpiryProcessingErrors` | warning | `rate(expiry_sweep_resolved_total{outcome=~"getbyid_error\|marshal_error\|outbox_error\|transition_error"}[5m]) > 0 for 5m` — D6 每筆 row resolve 失敗率。跟 `ExpiryFindErrors`(「掃不到表」)和 MaxAge 標記分開。抓的是暫時性的資料庫鎖競爭、outbox 寫入失敗、GetByID 抖動。下個 sweep 自動重試。 |
| `ExpiryFindErrors` | critical | `rate(expiry_find_expired_errors_total[5m]) > 0 for 2m` — D6 資料庫盲點。同時涵蓋 `FindExpiredReservations` 跟 `CountOverdueAfterCutoff` 查詢失敗。**這個告警觸發時,`expiry_oldest_overdue_age_seconds` 跟 `expiry_backlog_after_sweep` 會被「保留在 last-known-good」**(round-3 F2 契約)— 那些讀數可能是事故前的舊值。先查 Postgres 健康度 / 連線池 / migration 000012 partial index。 |
| `ExpiryMaxAgeExceeded` | critical | `increase(expiry_max_age_total[1h]) > 0` — D6 單筆即 page,**純通知用**。reservation 過了 `EXPIRY_MAX_AGE`(預設 24h)後才被 sweeper 抓到。Row 現在已經是 `expired`(round-1 P1 契約:D6 永遠 expire,MaxAge 純為觀測)— 重點是要查為什麼這麼久才處理到。檢查 sweeper uptime、部署日誌、k8s CronJob 觸發紀錄。**不要直接動這個 row,sweeper 已經做完它的工作了。** |

> **Worker process 的 metric 抓取 — 已由 O3 後續 PR 補齊。** 上面的 `recon_*`、`saga_watchdog_*`、`expiry_*`,以及 saga 的 `db_*` / `redis_*` 失敗計數器,都是註冊在 `booking-cli {recon,saga-watchdog,expiry-sweeper}` 這些 worker process 各自的 default Prometheus registry 裡。每一個 binary 都會在 `:9091` 開一個 metrics-only HTTP listener(可透過 `WORKER_METRICS_ADDR` 環境變數設定;設為空字串就關掉,適用於 `--once` CronJob 模式),`prometheus.yml` 也有對應的 scrape job(`recon`、`saga-watchdog`、`expiry-sweeper`)。要確認可以在 Prometheus → Graph 用 `up{job=~"recon|saga-watchdog|expiry-sweeper"} == 1` 驗證;listener 同時也提供 `/healthz`,compose 的 `HEALTHCHECK` 直接用同一個 port 即可。
>
> **D7(2026-05-08)把 `payment-worker` scrape job 跟 `payment_worker` binary 一起刪掉了**。`kafka_consumer_retry_total` 仍然有 emit(現在只剩 in-process 跑在 `app` 裡的 saga consumer 在 `order.failed` 上 emit),metric 的 label 集合從 `{topic=order.created|order.failed}` 縮成只剩 `{topic=order.failed}`,改由 `booking-service` job(`app:8080/metrics`)抓取。原有的 `KafkaConsumerStuck` alert 繼續正常運作。

### 故意把告警觸發起來(測試)

要驗證告警的整條管線是否通,把監測對象的指標推到超過門檻 + 撐過 `for:` 窗口。範例:

```bash
# OrdersStreamBacklogYellow — 把 > 10K 條目推進 orders:stream
docker exec booking_redis redis-cli eval \
  "for i=1,11000 do redis.call('XADD','orders:stream','*','probe',i) end return 1" 0
# 等 2-3 分鐘(告警的 `for: 2m`),然後看 Prometheus → Alerts。

# OrdersDLQNonEmpty — 推一條進 DLQ
docker exec booking_redis redis-cli XADD orders:dlq '*' probe 1
# 等 5 分鐘以上(告警的 `for: 5m`)。

# RedisStreamCollectorDown — 讓 Redis 暫時掛掉
docker compose stop redis
# 2m 後告警 fires;`docker compose start redis` 在下一個 scrape 內就把它清掉。

# SagaStuckFailedOrders — 把一筆 Failed 訂單的 updated_at 倒回去,讓它越過 SAGA_STUCK_THRESHOLD。
# 直接 UPDATE 比等自然 Failed→Compensated 卡住更可靠 — 因為 saga consumer 會在毫秒內補償,
# 否則沒辦法製造出 stuck 狀態。
docker exec booking_db psql -U user -d booking -c \
  "UPDATE orders SET status='failed', updated_at = NOW() - INTERVAL '5 minutes' WHERE id = '<某個既有訂單的 uuid>';"
# Watchdog 預設 60s sweep 一次 + 告警 `for: 10m`,所以等 ~11m 後去 Prometheus → Alerts 確認。
# 清理:把該列 UPDATE 回原本狀態,或讓 watchdog 自己重新觸發 compensator
#(它會把 Failed → Compensated,因為這筆資料沒有真正的 reverted Redis key 紀錄)。

# TargetDown — 把某個 worker 停掉,等 2m+,看 Prometheus → Alerts → TargetDown 觸發。
docker stop booking_recon
# 等 2m+(告警 `for: 2m`)。
# 清理:docker start booking_recon → up 在一個 scrape(15s)內就會回到 1。

# OutboxPendingBacklog — 把 relay 的 publish 路徑擋住,讓塞進去的列卡在 pending。
# D7(2026-05-08)之後,舊的食譜(直接 INSERT 假列等 alert 自己觸發)失效:
# relay 不會驗證 payload 形狀,Kafka auto-topic-creation 接受寫入,`processed_at`
# 在一個 tick 之內就會被設上 → gauge 在 alert 的 `for: 5m` 視窗 arm 起來之前
# 就掉回門檻以下了。
#
# 先擋住 Kafka,再注入:
docker compose stop kafka
docker exec booking_db psql -U booking -d booking -c \
  "INSERT INTO events_outbox (id, event_type, payload, status)
   SELECT gen_random_uuid(), 'order.failed', '{\"probe\":true}'::jsonb, 'PENDING'
   FROM generate_series(1, 200);"
# Kafka 一掛,relay 的 Publish() 就會失敗,MarkProcessed 跳過,列累積起來。
# `outbox_pending_count` 越過 100 門檻,並維持高於門檻直到 Kafka 回來為止。
# 等 ~6m(告警 `for: 5m`)。
# 清理 — 先 DELETE 假列,再把 Kafka 開回來。如果反過來先開 Kafka,relay
# 下一次輪詢會搶在 DELETE 之前把這些假 payload publish 出去並標記
# processed,saga consumer 收到沒有真實 order_id 的 `order.failed` 訊息後
# 會 retry-storm 或進 DLQ。順序:
#   docker exec booking_db psql -U booking -d booking -c \
#     "DELETE FROM events_outbox WHERE payload->>'probe' = 'true';"
#   docker compose start kafka
# (Kafka 一回來,relay 在數秒內就會把真正 pending 的列追上;假列在那之前
# 就已經被刪掉了。)

# OutboxPendingCollectorDown — 把 postgres 短暫停掉讓 COUNT 查詢失敗。
# 等 ~2m 後 OutboxPendingCollectorDown 觸發;postgres 回來後一個 scrape 內就會解除。
docker compose stop postgres
# 等 2m+。清理:docker compose start postgres。

# InventoryDriftDetected — 直接修改 Redis 讓它跟資料庫不一致,等 recon 程序的
# 漂移 sweep 把它抓出來。先從 `SELECT id FROM event_ticket_types WHERE available_tickets > 0 LIMIT 1`
# 拿到一個已知的 ticket_type_id,然後:
docker exec booking_redis redis-cli SET ticket_type_qty:<ticket-type-uuid> 999999
# 預設 sweep interval 是 60s + 告警 `for: 5m`,所以等 ~6m 再到 Prometheus → Alerts 看。
# Metric 會以 direction=cache_high 觸發(Redis 大於資料庫且超過容忍值)。
# 清理:重啟 app 讓 rehydrate 重跑(docker compose restart app),
# 或手動 `SET ticket_type_qty:<uuid> <資料庫的 available_tickets>` 把值補回來。

# InventoryDriftListEventsErrors / InventoryDriftCacheReadErrors —
# 把對應的依賴短暫停掉。兩個都是 rate counter 告警,所以斷線期間
# 每次 scrape 都會 +1。
docker compose stop postgres   # → 2m 後 InventoryDriftListEventsErrors 觸發
docker compose stop redis      # → 5m 後 InventoryDriftCacheReadErrors 觸發
# 清理:docker compose start <service>;counter 停止增加,rate 視窗
# 過期後告警自動解除。

# SweepGoroutinePanic —沒辦法乾淨地觸發(這個 counter 只有真的 Go panic
# 才會 +1)。本機驗證的話,在 `recon.Reconciler.Sweep` 或
# `recon.InventoryDriftDetector.Sweep` 開頭暫時塞 `panic("smoke")`,
# build 後跑 > 1 個 sweep interval,觀察
# `sweep_goroutine_panics_total{sweeper=...}` 有沒有 +1。**絕對不要把
# 注入的 panic commit 上去。** 驗證完先 revert。
```

測試完還原:`docker exec booking_redis redis-cli DEL orders:stream orders:dlq`(會把進行中的 production 資料一起殺掉 — 只能在開發環境做)。

---

## 6. 實用迴圈

日常 senior 工程師的用法:

1. **Grafana** — 「現在有沒有東西閃紅?」
2. **Prometheus** — 「我來寫個 query 調查」
3. **原始 `/metrics`** — 「這個指標到底有沒有被吐出來?」(PR 階段的正確性檢查)
4. **App 日誌**(`docker compose logs -f app`) — 拿背景脈絡 + correlation ID,光靠指標看不到的東西

日誌與指標是相連的:每一行結構化 log 都帶 `correlation_id`,以及(被取樣到時)`trace_id`/`span_id`。Grafana 上看到一個尖峰,把時間戳記下來,在 Jaeger(http://localhost:16686)搜該時段的 trace,把對應的 `correlation_id` 抓出來去 grep app 日誌。串線細節見 [internal/log/](../internal/log/)。

### 一鍵飽和診斷 — `make profile-saturation`

當你要回答的不是「現在有沒有閃紅」,而是 **「為什麼吞吐量在 X req/s 撞牆?」** 時,用 `make profile-saturation`。它在跟 `benchmark-compare` 同樣的條件下跑 k6,然後在峰值瞬間同時抓 pprof + Redis `INFO commandstats` + SLOWLOG + Prometheus 訊號快照,全部丟到 `docs/saturation-profile/<timestamp>_c<vus>/`。

```bash
# 預設:VUS=500 DURATION=60s — 跟我們的 k6 baseline 是同一個條件。
make profile-saturation

# 也可以覆寫:
make profile-saturation VUS=1000 DURATION=90s
```

輸出會給你這些東西:

| 檔案 | 回答的問題 |
| :-- | :-- |
| `cpu.pprof`(30s 視窗) | Go 的 CPU 花在哪裡。`go tool pprof -http=:0 cpu.pprof` 會打開 flame graph。 |
| `heap.pprof`、`goroutine.pprof` | 配置點的 in-use bytes、goroutine 數量 + 堆疊。 |
| `commandstats_diff.txt` | 視窗內 Redis 各指令依累計 μs 排名 — 「Redis 在做什麼?」的答案。 |
| `slowlog.txt` | 單次 > 10ms 的指令。空的 = 沒有單一慢操作。 |
| `prom_signals.json` | 視窗結束時的 RED + USE 訊號快照:Redis CPU rate、client 連線池 hits/misses/timeouts、PG pool waits、p99 latency、goroutine 數、accepted-bookings/sec。 |
| `k6_summary.txt` | 從 load generator 角度看的吞吐量 + 各百分位延遲。 |
| `README.md` | 自動生成的決策樹。讀完 `cpu.pprof` 後,把「Findings」段落補上去。 |

自動生成 README 裡的決策樹就是 senior 級的做法:**先量測,再優化**。我們抓的第一份 profile (`docs/saturation-profile/20260502_221629_c500/`)就是標準範本 — 它證偽了「Redis 是瓶頸」這個假設(Redis CPU 在峰值只有 53%),指出真正的天花板是網路 syscall I/O,也驗證了在 PR #69(B3 庫存分片)被任何下游 PR 寫出來之前就把它撤掉的決定。

執行這個指令需要:
- `.env` 裡 `ENABLE_PPROF=true`,讓 app 的 pprof listener 是開的。
- `grafana/k6` docker image(第一次跑時 script 會自動拉 — 多 ~30s)。
- 完整 compose stack 起來且暖機完成(若 `app`、`redis`、`prometheus`、`redis_exporter` 任何一個沒起來,script 會自動 `docker compose up -d`)。

---

## 7. 什麼時候要更新這份指南

這份文件**配對於**真正的觀測性程式碼。對下面任一介面做修改都需要連帶更新這份指南(以及它的英文版):

| 介面 | 檔案 |
| :-- | :-- |
| 指標註冊 | [internal/infrastructure/observability/metrics.go](../internal/infrastructure/observability/metrics.go) |
| 自訂 collector | `internal/infrastructure/observability/*_collector.go` |
| 告警規則 | [deploy/prometheus/alerts.yml](../deploy/prometheus/alerts.yml) |
| Prometheus scrape config | [deploy/prometheus/prometheus.yml](../deploy/prometheus/prometheus.yml) |
| Grafana 儀表板 | `deploy/grafana/provisioning/dashboards/*.json` |

PostToolUse hook([.claude/hooks/check_monitoring_docs.sh](../.claude/hooks/check_monitoring_docs.sh))會在上述任一檔案被編輯時觸發,塞一段提醒進對話,讓 Claude 在收尾前先把這份指南更新好。

如果你不會翻譯,問人類作者,別跳過 zh-TW 那邊的更新 — 結構對齊比完美的中文文筆重要。
