# Architectural Backlog

Items surfaced during the 2026-04-24 service-by-service review that are **deliberately deferred** until the code-level review pass is complete. Each entry captures:

- **What** the option is
- **Why** it came up
- **Trade-off** / cost estimate
- **Current stance** — picked? pending decision? ruled out?

This is a discussion document, not a task list. Nothing here should be actioned without an explicit decision first.

---

## 1. Horizontal scaling benchmark on k8s

**What**: Prove the current modular monolith can scale horizontally before entertaining any microservices talk.

**Why it came up**: Question was raised about pursuing microservices for higher throughput. Core clarification: microservices usually **reduce** throughput (network hops, mTLS, serialization overhead, distributed-consistency patterns). The correct first lever is horizontal scaling the existing monolith, which this project was designed for.

**System readiness**: 100% — app is stateless, `OutboxRelay` has `pg_try_advisory_lock(1001)` leader election, Redis Streams consumer groups handle worker coordination, Nginx already in `docker-compose`, graceful shutdown via fx lifecycle.

**Proposed design**:

| Variable | Values |
|----------|--------|
| `booking-api` replicas | 1, 2, 4, 8, 16 |
| `payment-worker` replicas | 2 (fixed) |
| k6 load | VUs=2000, duration=60s |
| Inventory | unlimited (avoid sold-out skewing results) |

Measure:
- Peak RPS, p50 / p95 / p99 latency
- Pod CPU, memory
- Scaling efficiency (ideal = linear; realistic = 70–90%)
- First bottleneck (Redis CPU / Postgres CPU / Kafka network / Worker lag)

**Effort**: ~1–2 days (k8s manifests + benchmark script + report).

**Status**: Not started. Should land before any architectural migration decision.

---

## 2. Microservices migration — **ruled out for this project**

**Decision**: Booking_monitor stays as a modular monolith. Microservices learning to be done on a **separate project** where the domain actually justifies it (multi-team, multi-runtime, multi-language).

**Reasoning**:
- Microservices solve **independent deployability** + **team autonomy**, not throughput. Both benefits are invisible on a single-author project.
- Throughput typically degrades after splitting: network hops, serialization, distributed transactions, connection-pool explosion.
- All subsystems here are CPU-bound Go code — no scaling-dimension differentiation that would justify splitting.
- `docker-compose` already deploys `booking-cli server` + `booking-cli payment` as separate processes → **partial process-per-module shape already achieved**.

**If this is revisited**, the natural progression path is:

```
Today     → Modular monolith, 2 processes (server, payment)
Mid-term  → Process-per-module (api / worker / outbox / saga / payment),
            same binary, same repo, different cobra subcommands
Long-term → Separate repos, separate release cycles, real microservices
```

The mid-term step captures ~90% of the benefit at ~10% of the cost. That's the right next step whenever "independent deployment of individual components" becomes a real need — not before.

---

## 3. Order lifecycle tracking — **audit log planned here**, Event Sourcing out of scope

**Decision**: Add an append-only `order_events` audit-log table to booking_monitor. Event Sourcing is **not** pursued here.

**Design**:

```sql
CREATE TABLE order_events (
    id          BIGSERIAL PRIMARY KEY,
    order_id    BIGINT NOT NULL,
    event_type  TEXT NOT NULL,            -- 'CREATED', 'PAID', 'FAILED', 'COMPENSATED'
    payload     JSONB,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON order_events (order_id, occurred_at);
```

- `WorkerService`, `PaymentService`, `SagaCompensator` each `INSERT` in the **same transaction** as the state change (not a separate write).
- Current-state queries keep using `orders.status` (fast).
- Historical / audit queries use `order_events`.

**Effort**: ~1 day (1 migration + 3 service insertions).

**Event Sourcing rejected because**:
- Order lifecycle has only 3–5 states — no complexity that justifies event replay
- No regulatory / compliance requirement for immutable audit
- No "time travel" requirement
- Team (single author) has limited bandwidth for ES operational overhead (schema evolution, snapshotting, read-model projections)
- Rule of thumb: ES without CQRS is usually a mistake; CQRS without ES is fine; ES + CQRS is an org-level commitment.

If Event Sourcing learning is a goal → new project, new domain (insurance claims, ledger, regulatory audit) that actually justifies it.

---

## 4. Saga observability + reliability gaps

Current Saga implementation ([`saga_compensator.go`](../internal/application/saga_compensator.go) + [`saga_consumer.go`](../internal/infrastructure/messaging/saga_consumer.go)) is **mid-to-high industry standard**. It has durable retry counters, DLQ with provenance headers, idempotent compensation at both DB and Redis layers, and metric honesty (DLQ count only increments on successful DLQ write).

Gaps vs Temporal / Axon-tier:

| # | Gap | Impact | Cost |
|---|-----|--------|------|
| 1 | No explicit saga state log — can't inspect "saga X is at step N" directly | Low. State is implicit in `order.status` + Kafka offsets + Redis retry counter. | Medium — new `saga_state` table + writes at each step |
| 2 | **No saga timeout** — if `PaymentService` hangs forever (not fails), no event ever fires, compensation never triggers | **Real leak** | Medium — needs watchdog service or per-order TTL |
| 3 | No exponential backoff on compensation retry | Small — Kafka rebalance provides implicit backoff | Small |
| 4 | No saga-level metrics (`saga_duration_seconds`, `saga_compensation_success_ratio`) | Low — `SagaPoisonMessagesTotal` + `DLQMessagesTotal` cover failure modes | Small — 1 Prometheus histogram |
| 5 | DLQ messages require manual intervention but no alert rule exists | **Real leak** — inconsistent state can linger indefinitely | Small — yaml alert rule |

**Proposed bundling**: A "Saga observability + watchdog" PR covering #2, #4, #5 would be <100 lines. Items #1 and #3 deferred to later if a second saga type ever appears.

---

## 5. Reconciliation job for Redis / DB drift

At-least-once + idempotent compensation permits transient drift: e.g. DB rollback commits, Redis revert fails, message retries, DB short-circuits (idempotency check), Redis retries, Redis also fails after max retries → DB says `COMPENSATED`, Redis still has `-N` inventory. No current mechanism detects or corrects this.

**Proposal**: Periodic k8s `CronJob` (hourly) that compares:

```
events.total_tickets - SUM(orders.quantity WHERE status IN ('paid','pending'))
                 vs
Redis event:{id}:qty
```

and emits a `inventory_drift_gauge` Prometheus metric. Human-triggered reconciliation on alert (self-healing is dangerous; alert + manual is safer).

**Effort**: Small — ~50 LoC script + one Prometheus gauge + alert rule.

---

## 6. Command vs Event discipline — **already correct, no action**

Concern raised about whether the whole system should move to a unified event format (even API → Redis).

**Clarification**: the command / event split the system already uses is industry-standard:

| Type | When | Example in this repo |
|------|------|-----------------------|
| Command (sync RPC) | Caller needs result immediately, can't wait | `API → Redis Lua deduct`, `Worker → Postgres insert`, `Payment → Gateway` |
| Event (async message) | Caller doesn't wait, multiple consumers possible | `Worker → Kafka order.created`, `Payment → Kafka order.failed` |

Unifying everything to events would make the hot path async (user waits for Redis deduct result ≈ unavoidable), inflate storage (every deduct now produces a persistent event), and make debugging harder. **No change needed.**

---

## 7. 5-layer reliability stack — 3 layers strong, 2 layers partial

When the reliability question was discussed, the layering came out as:

| # | Layer | Status in this project |
|---|-------|-------------------------|
| 1 | At-least-once delivery | ✅ Kafka `acks=all`, Redis Stream PEL |
| 2 | Idempotent consumers | ✅ DB unique constraints, Redis SETNX, saga status check |
| 3 | Write-ahead logging | ✅ Postgres WAL, Kafka partition log, outbox table (de facto WAL) |
| 4 | Replication | ⚠️ Kafka 3 brokers (✅), Postgres streaming replica (❌), Redis AOF + replica (❌) |
| 5 | Observability + human intervention | ⚠️ Prometheus metrics (✅), DLQ alert rules (❌), reconciliation job (❌) |

**Layer 4 remediation** is infrastructure work (deploy Postgres replica, configure Redis persistence). Orthogonal to code changes — covered by a future deploy PR, not an app PR.

**Layer 5 remediation** is partially covered by items #4 and #5 above (alert rules + reconciliation). Both are small, high-leverage additions.

---

## Suggested ordering (when ready to revisit)

The code-level review pass comes first. Once that completes, a reasonable sequence:

1. **Audit log table** (item #3) — 1 day, unblocks traceability without architectural change
2. **Saga observability + watchdog** (item #4, covers #2 + #4 + #5) — <100 LoC, high reliability payoff
3. **Reconciliation CronJob** (item #5) — small, closes the long-tail drift gap
4. **k8s horizontal scaling benchmark** (item #1) — gives data-driven answer to the "when do we need to scale" question
5. **Postgres replica + Redis persistence** (item #7 layer 4) — deploy-side, not app-side
6. **Microservices talk revisited only after all of the above**, if ever

---

## Related documents

- [PROJECT_SPEC.md](PROJECT_SPEC.md) — full system specification
- [scaling_roadmap.md](scaling_roadmap.md) — the scaling-stage progression this project has already walked
- [architecture/current_monolith.md](architecture/current_monolith.md) — current architecture diagram
- [architecture/future_robust_monolith.md](architecture/future_robust_monolith.md) — the post-hardening target we're already close to
