# Stage 5 Interview Demo Script

> Saved 2026-05-19. Demo length: 20–30 min. Audience: senior backend / architect interview.

---

## 0. 面試前一晚準備

```bash
docker-compose up -d
make migrate-up
curl http://localhost/readyz
# {"checks":[{"name":"postgres","ok":true},{"name":"redis","ok":true},{"name":"kafka","ok":true}],"status":"ok"}
```

**備用環境**：三個終端視窗
- A：`docker-compose logs -f app`（即時 log）
- B：curl 指令
- C：`make bench-up` + k6

Grafana 開著 `http://localhost:3000`，先把 dashboard 調好。

---

## 1. 架構 Walkthrough（5 min）

開口框架：

> 「Stage 5 是這個系統的 production-standard，核心決策是把 Kafka produce 放進 booking hot path——用 38% 吞吐量換 crash-safe intake，類似 Damai / Ticketmaster 的做法。」

```
Client
  → nginx (rate-limit)
  → KafkaIntakeService.BookTicket()
      ├─ Redis Lua deduct.lua (atomic inventory reservation)
      └─ Kafka produce → booking.intake.v5   ← durability gate
           ↓
      IntakeConsumer.Start()
           ↓
      worker.MessageProcessor
           ├─ UoW: orders INSERT + event_ticket_types DECREMENT
           └─ Redis stream ACK
```

**為什麼 Kafka 而非 Redis Stream（Stage 3）？**

> 「Stage 3 在 Redis crash 後 stream 消失，intake 完全 silently drop。Kafka 有 replication + at-least-once delivery；加上 SETNX 冪等鍵（`booking:processed:{order_id}`），PEL retry 不會 double-write。」

代碼指引（30 秒掃描）：
- `cmd/booking-cli-stage5/server.go:58-66` — fx.Provide 依賴鏈
- `internal/application/booking/intake.go` — `IntakePublisher` / `IntakeConsumer` interface
- `internal/infrastructure/messaging/kafka_intake_consumer.go` — 消費 + Ping 健康檢查

---

## 2. Happy Path Live Demo（5 min）

```bash
# 1. 建 event + ticket type
EVENT=$(curl -s -X POST http://localhost/api/v1/events \
  -H 'Content-Type: application/json' \
  -d '{"name":"Demo Concert","total_tickets":500,"price_cents":2000,"currency":"TWD"}')
echo $EVENT | python3 -m json.tool

TT_ID=$(echo $EVENT | python3 -c "import sys,json; print(json.load(sys.stdin)['ticket_types'][0]['id'])")

# 2. 搶票 (Stage 5 同步 Kafka produce)
BOOK=$(curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: demo-$(date +%s)" \
  -d "{\"user_id\":1,\"ticket_type_id\":\"$TT_ID\",\"quantity\":2}")
echo $BOOK | python3 -m json.tool
ORDER_ID=$(echo $BOOK | python3 -c "import sys,json; print(json.load(sys.stdin)['order_id'])")

# 3. Worker async 處理中... poll 狀態
curl -s http://localhost/api/v1/orders/$ORDER_ID | python3 -m json.tool
# status: awaiting_payment ← worker 已寫入 PG

# 4. 付款意圖
PAY=$(curl -s -X POST http://localhost/api/v1/orders/$ORDER_ID/pay)
echo $PAY | python3 -m json.tool

# 5. 支付成功
curl -s -X POST "http://localhost/test/payment/confirm/$ORDER_ID?outcome=succeeded"
curl -s http://localhost/api/v1/orders/$ORDER_ID | python3 -m json.tool
# status: paid ✓
```

**重點口語**：

> 「`/book` 的 202 回來時，Kafka produce 已經 ack——就算這台機器馬上 crash，order 也在 Kafka 裡，重啟後 consumer 會繼續處理。這是 Stage 3 沒有的保證。」

---

## 3. Saga 失敗路徑 Demo（5 min）

```bash
BOOK2=$(curl -s -X POST http://localhost/api/v1/book \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: demo-fail-$(date +%s)" \
  -d "{\"user_id\":2,\"ticket_type_id\":\"$TT_ID\",\"quantity\":1}")
ORDER2=$(echo $BOOK2 | python3 -c "import sys,json; print(json.load(sys.stdin)['order_id'])")

sleep 2
curl -s -X POST http://localhost/api/v1/orders/$ORDER2/pay > /dev/null

# 支付失敗 → Saga 補償
curl -s -X POST "http://localhost/test/payment/confirm/$ORDER2?outcome=failed"

# 驗證
curl -s http://localhost/api/v1/orders/$ORDER2 | python3 -m json.tool
# status: compensated

# Redis 庫存還原
docker exec booking_redis redis-cli GET "inventory:ticket:$TT_ID"

# saga_compensations 冪等記錄
docker exec booking_db psql -U booking -c \
  "SELECT compensation_id, redis_reverted_at FROM saga_compensations ORDER BY completed_at DESC LIMIT 3;"
```

**最有深度的部分**：

> 「這裡有一個雙重失敗視窗：補償成功寫進 PG，但 Redis key 消失（crash）。Kafka 重送同一個 `order.failed`，naive 實作會再 INCRBY 一次，庫存多出來但 PG 是 0。我的解法是 saga_compensations table——補償完成後寫入 PG，下次 Kafka retry 時先查這張表，`redis_reverted_at IS NOT NULL` 就跳過 Redis revert。PG 是 SSOT，Redis 的 SETNX key 是 defense-in-depth。」

---

## 4. Benchmark + Grafana 監控解讀（10 min）

### 啟動 benchmark

```bash
make bench-up
k6 run \
  -e API_ORIGIN=http://localhost:8095 \
  -e VUS=500 -e DURATION=60s -e TICKET_POOL=500000 \
  scripts/k6_intake_only.js
```

### Grafana 解讀順序

**Panel 1 — Booking Accepted Rate**
```promql
sum(rate(accepted_bookings_total[10s]))
```
> 「Redis Lua deduct 成功速率，約 5,500/s。對比 Stage 3 的 8,400/s——差的 2,900/s 就是 Kafka produce 的 durability tax。」

**Panel 2 — Kafka Produce Duration**
```promql
histogram_quantile(0.95, rate(kafka_produce_duration_seconds_bucket[10s]))
```
> 「p95 約 90–120ms。這就是為什麼 Stage 5 book→reserved p95 是 1 秒——Kafka ACK 在 hot path 上。」

**Panel 3 — Redis Lua Op Histogram**
```promql
histogram_quantile(0.99, rate(redis_lua_op_duration_seconds_bucket{op="deduct"}[10s]))
```
> 「deduct.lua p99 < 5ms，不是瓶頸。瓶頸是 Kafka。」

**Panel 4 — Worker Consumer Lag**
```promql
kafka_consumer_group_lag{group="booking-intake-v5"}
```
> 「500 VU 下 lag 在 2,000–5,000 震盪，代表消費端追得上。如果 lag 持續成長才是問題。」

**Panel 5 — Saga Compensation**
```promql
rate(saga_compensator_events_processed_total[30s])
```
> 「正常接近 0。trigger 一個 failed payment，這個數字會跳一下。」

### 關鍵數字

```
Stage 4 → Stage 5 tradeoff:
┌─────────────────┬──────────┬──────────┬────────┐
│                 │ Stage 4  │ Stage 5  │   Δ    │
├─────────────────┼──────────┼──────────┼────────┤
│ intake 吞吐     │ 8,378/s  │ 5,533/s  │ −34%   │
│ intake p(95)    │ 33.6 ms  │ 111.9 ms │ +3.3×  │
│ full-flow crash │  YES     │   NO     │ ✓      │
│ 資料遺失        │  有可能  │   不可能 │ ✓      │
└─────────────────┴──────────┴──────────┴────────┘
```

---

## 5. 可能面試問題備忘

| 問題 | 核心答案 |
|---|---|
| 為什麼不用 Redis Stream 就好？ | Redis crash → stream 消失，at-least-once 沒保障；Kafka 有 replication + consumer group offset |
| SETNX 和 saga_compensations 重複？ | 各守一個 failure window：SETNX 守 Redis 層毫秒級 retry，PG 守 Kafka redelivery 可能跨 crash |
| Worker 的 PEL 是什麼？ | Redis Stream pending entry list；consumer crash 時未 ACK 的消息；Stage 5 啟動先掃 PEL 重播 |
| Stage 5 full_flow p95 為什麼 1000ms？ | 兩層非同步：Kafka produce ACK (~100ms) + worker 處理 + DB write |
| 怎麼監控 Stage 5 健康？ | `/readyz` (Kafka ping) + Grafana consumer lag + saga rate + drift detector sweep |
| Kafka broker down 怎麼辦？ | `intakeConsumer.Ping()` OnStart 失敗 → fx.Shutdown → k8s restart；`/readyz` 503 → pod 下線 |

---

## 6. 面試當天 Checklist

```
□ docker-compose up -d && make migrate-up
□ curl /readyz → 全 ok
□ Grafana 3000 dashboard 開著
□ 三個 terminal 視窗就位
□ ~/demo.sh 可貼可執行
```

**強說話句型**：

> **「這個設計是 trade-off，不是 bug。」**

38% throughput tax 換 at-least-once durability——這才是架構師的語言。
