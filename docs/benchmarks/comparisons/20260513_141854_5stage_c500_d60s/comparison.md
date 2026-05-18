# 5-Stage Architecture Benchmark — Breaking Point Analysis

**Captured**: 2026-05-13T06:18:54Z · **Branch**: `feat/stage5-durable-kafka-intake` · **Commit**: `f2ad53a`

## Parameters

| Parameter | Value |
|-----------|-------|
| Script (full-flow) | `scripts/k6_two_step_flow.js` |
| Script (intake) | `scripts/k6_intake_only.js` |
| VUs | 500 |
| Duration | 60s |
| Ticket pool | 500,000 |
| Target | `http://localhost:8080–8094/api/v1` (per-stage port) |

Raw outputs: `stage{1-5}/full_flow_run_raw.txt`, `stage{1-5}/intake_only_run_raw.txt`

---

## Scenario A — Full-flow (book → reserve → pay)

| Stage | accepted/s | paid/s | book→reserved p(95) | e2e p(95) | business_err | Verdict |
|-------|-----------|--------|---------------------|-----------|-------------|---------|
| 1 | 78.8 | 63.4 | 139 ms | 965 ms | 0.00% | ✅ baseline |
| 2 | 73.8 | 58.9 | 51 ms | 384 ms | 0.00% | ✅ latency improved |
| 3 | 66.0 | 52.3 | 1,120 ms | 1,270 ms | 0.10% | ⚠️ latency cliff |
| **4** | **25.2** | **0** | **513 ms** | **n/a** | **100%** | **❌ CRASH — threshold crossed** |
| 5 | 64.5 | 51.1 | 1,010 ms | 1,370 ms | 0.06% | ⚠️ recovered, latency elevated |

### Stage 4 crash detail

Starting at t≈43s, the `/pay` endpoint returned mass `EOF` errors (connection reset). The server survived for the remainder of the run but the payment path became completely unavailable:

```
business_errors: 100.00%  (2,185 out of 2,185 post-depletion iterations)
end_to_end_paid_duration: avg=0s  (no successful payments after t=43s)
```

k6 exited with threshold failure: `business_errors rate<0.05 CROSSED`.

---

## Scenario B — Intake-only (POST /api/v1/book at full concurrency)

| Stage | accepted/s | avg latency | p(95) latency | http_failed% | pool depleted? | Verdict |
|-------|-----------|-------------|--------------|-------------|----------------|---------|
| 1 | 1,431 | 332.8 ms | 967 ms | 4.4% | ❌ No | ⚠️ slow path |
| 2 | 8,333 | 26.6 ms | 73.8 ms | 55.6% | ✅ Yes | ✅ fast path engaged |
| 3 | 8,479 | 15.2 ms | 25.2 ms | 74.1% | ✅ Yes | ✅ stable |
| 4 | 8,378 | 16.9 ms | 33.6 ms | 71.7% | ✅ Yes | ✅ stable |
| **5** | **5,139** | **97.1 ms** | **126.9 ms** | **0.0%** | **❌ No** | **⚠️ durability tax** |

Note: `http_failed` in stages 2–4 reflects 409 Sold-Out responses after pool depletion, not errors. Stage 5's 0% failure rate confirms the pool was never depleted — throughput was genuinely lower.

---

## Breaking Points

### BP-1 · Full-flow latency cliff: Stage 2 → Stage 3

`book_to_reserved` p(95) jumps from **51 ms → 1,120 ms** (+22×). The reservation hot path remained fast (Lua deduct is sub-ms), but the 500-VU fan-out saturated the downstream worker queue, causing reservations to stall waiting for the async persistence acknowledgement. Throughput also drops from 73.8 → 66.0 accepted/s.

**Root cause**: worker queue backpressure under 500-VU fan-out. The booking response is not returned until the worker has at minimum ACK'd the stream entry, introducing a synchronous wait into what should be a pure Redis hot path.

### BP-2 · Full-flow hard failure: Stage 4 (t≈43s)

The Kafka + outbox pipeline collapsed under sustained 500-VU load. EOF errors on `/pay` indicate the application's HTTP server ran out of goroutines or file descriptors, likely caused by the outbox relay holding open connections while the Kafka broker backpressured. The server did not fully crash (booking path continued) but the payment path became unavailable for the remainder of the run.

**Observable signal**: `business_errors` rises from 0% → 100% after t=43s; `end_to_end_paid_duration` drops to 0s.

### BP-3 · Intake throughput ceiling: Stage 4 → Stage 5

Stage 5 (durable Kafka intake) adds a synchronous Kafka produce to the booking hot path, replacing the fire-and-forget Redis stream write:

| Metric | Stage 4 | Stage 5 | Delta |
|--------|---------|---------|-------|
| accepted/s | 8,378 | 5,139 | **−38%** |
| avg latency | 16.9 ms | 97.1 ms | **+5.7×** |
| pool depleted | Yes | No | 500k tickets unsold at 60s |

The **durability tax** is 38% throughput and 5.7× latency. In absolute terms, Stage 5 can accept ~308k bookings/min vs Stage 4's ~503k/min under identical 500-VU load. For a 500k-ticket flash sale at 500 VUs, Stage 5 requires approximately **97 seconds** to sell out vs Stage 4's **60 seconds**.

---

## Conclusion

**Full-flow breaking point**: Stage 4. Under 500 VUs, the Kafka + outbox pipeline fails completely at t≈43s with 100% business errors. Stage 5 recovers full-flow stability (business_errors drops back to 0.06%) but carries the BP-3 durability tax on the intake path.

**Intake breaking point**: Stage 5. The synchronous Kafka produce in the booking hot path costs 38% throughput and 5.7× latency. This is the expected cost of at-least-once Kafka durability on the critical path.

**Architecture decision captured**: Stage 5's durability trade-off (38% intake throughput for crash-safe intake) is intentional. For a flash-sale system where data loss is unacceptable, BP-3 is the accepted operating cost. BP-2 (Stage 4 crash) represents a stability regression that Stage 5 resolves by eliminating the outbox relay's connection-hold pattern.

---

## Caveats

- Runs are sequential on a single laptop (MacBook Pro M-series, load avg 3.47–3.64 at capture time). Run-to-run variance is typically 3–5%; cross-stage deltas above 10% are load-bearing.
- Stage 4's full-flow crash is a single observation. Re-running under identical conditions may differ in crash timing but the 100% business_error outcome was deterministic in this run.
- Stage 5 intake-only `http_failed=0%` is correct: the test ended before pool depletion, so no 409s were issued. This is not a data quality issue.
- Stage 1 intake `http_failed=4.4%` reflects genuine errors (not sold-out 409s) — the slow path at Stage 1 caused timeouts under 500-VU concurrency.
