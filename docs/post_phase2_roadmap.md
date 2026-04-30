# Post-Phase-2 Roadmap

Forward-looking sprint plan, written 2026-04-30 immediately after the Phase 2 checkpoint review. Supersedes prior memory-only roadmaps for sequencing decisions. Historical architecture evolution lives in [`scaling_roadmap.md`](scaling_roadmap.md) and is not duplicated here.

## Where we are

- **Phase 2 done**: PRs #45 (charging two-phase intent log + reconciler) → #46 (streams obs + DLQ MINID) → #47 (response shape + GET /orders/:id) → #48 (N4 idempotency fingerprint) → #49 (A5 saga watchdog + checkpoint framework).
- **First project-review checkpoint completed**: [`docs/checkpoints/20260430-phase2-review.md`](checkpoints/20260430-phase2-review.md). Grade A−. Findings split between a focused cleanup PR (Critical + cheap-Important) and 8 deferred follow-up PRs.
- **Next user goal**: make the project **demo-able** as a real e-commerce flash-sale flow. User has chosen **Pattern A** (Stripe Checkout / KKTIX style: split `POST /book` → `POST /orders/:id/pay` with reservation TTL + webhook receiver). See backlog §13.1.

## Sequence

```
Phase 2.5 (cleanup, ~1 wk)
  CP1  cleanup PR (rows 1–9 from action plan)
  CP2  recon Config + Metrics interfaces + saga.Config (architecture row 10)
  CP3  observability/metrics.go split + middleware move (row 11)
  CP4  testcontainers integration suite for repositories.go (row 12 = roadmap N8)
  CP5  docs/runbooks/* + alerts runbook_url annotations (row 13 = roadmap N3)
  CP6  Alertmanager deployment + delivery target wiring (row 14 = roadmap N3)
  CP7  test-surface sprint: saga_compensator, handler coverage, outbox.Run (row 16)
  CP8  header-bearing N4 benchmark (row 17)
  CP9  Grafana dashboard panels for recon / saga / DLQ / DB pool / cache (row 18)

Phase 3 (demo readiness, ~3–5 wk) — Pattern A
  D1  schema migration: add orders.reserved_until + orders.payment_intent_id; new orders status `awaiting_payment`
  D2  domain state machine: pending → awaiting_payment → paid | expired | failed
  D3  POST /api/v1/book becomes a reservation (TTL 10–15 min); response includes payment_intent metadata
  D4  POST /api/v1/orders/:id/pay creates the PaymentIntent against the gateway adapter (Stripe-like)
  D5  POST /webhook/payment receives async outcome; webhook signature verification; idempotent against payment_intent_id
  D6  reservation expiry sweeper (mirrors A5 watchdog shape): scan `awaiting_payment` past `reserved_until`, transition → expired, revert Redis inventory via revert.lua
  D7  payment_worker stops being a saga consumer for the happy path. Saga compensator scope narrows to {expired, payment_failed}.
  D8  frontend (out of repo, optional for portfolio): Stripe Elements integration in a thin Next.js demo app under `web/` to make the demo recordable
  D9  load-test refresh: k6 scenario script for the new two-step flow; baseline capture
  D10 demo recording: 3-minute video walkthrough of (a) reservation, (b) intent creation, (c) webhook outcome, (d) expiry sweep, (e) Grafana dashboards lighting up

Phase 4 (production-hardening, ~2–3 wk) — only after Pattern A demo lands
  P1  Redis HA (A9) — Sentinel + FailoverClient
  P2  Cross-process circuit breakers (A10*) — Redis + DB + Kafka + payment
  P3  k8s manifest / Kustomize base (N7) + HPA + PDB + probes
  P4  Auth / RBAC + gosec baseline (N9)
  P5  Backup / DR runbook + retention policy (N10)

Phase 5 (capstone benchmark, ~2 wk) — measure real bottleneck
  B1  k8s horizontal-scale benchmark (locks in the architecture story for portfolio)
  B2  config-tunables targeted optimization based on B1 findings
  B3  inventory sharding (CONDITIONAL on B1 showing single-key Redis CPU saturation)
```

## Cleanup PR (CP1) scope

Rows 1–9 from the checkpoint action plan, grouped logically:

| Theme | Items | Files touched |
| :-- | :-- | :-- |
| **Correctness fix** | Reconciler max-age must emit `order.failed` outbox event before MarkFailed (DEF-CRIT) | `internal/application/recon/reconciler.go`, possibly new factory in `internal/application/order_events.go` |
| **Doc drift (already in this PR)** | D1 5xx → 2xx; D2/D3/D4/D5/D6 currency | PROJECT_SPEC, CLAUDE.md, README.md (× 2 each), memory files |
| **Wire-format fix** | `NewOrderFailedEventFromOrder` factory; remove `Version: 0` bug in watchdog | `internal/application/saga/watchdog.go`, `internal/application/order_events.go` |
| **Ops** | scrape `payment_worker` + `recon` (verify each binary serves `/metrics`); 6 missing alerts (`idempotency_cache_get_errors_total`, `recon_mark_errors_total`, four `*_failures_total` counters); `HighLatency` window 1m → 5m | `deploy/prometheus/prometheus.yml`, `deploy/prometheus/alerts.yml` |
| **Security** | Cap pagination `size` at 100; `OrderStatus.IsValid()` + use in `StatusFilter` | `internal/infrastructure/api/booking/handler.go`, `internal/domain/order.go`, `internal/infrastructure/api/dto/request.go` |

CP1 is intentionally narrower than "everything-Critical" because rows 10–14 each warrant their own reviewable PR (architecture refactor, testcontainers, runbooks, Alertmanager).

## Demo plan — Pattern A in detail

The demo should show the project doing **what real flash-sale e-commerce does**, not just a proof-of-concept. Pattern A is the shape Stripe Checkout, KKTIX, Ticketmaster all use.

### User-visible flow (3-minute demo script)

1. **Reservation**: client `POST /api/v1/book` → server returns 202 with `order_id`, `reserved_until`, `payment_intent_client_secret`. Redis inventory deducted; order in `awaiting_payment` state.
2. **Payment collection** (frontend): Stripe Elements card form mounted with `client_secret`. User submits card. Stripe.js confirms PaymentIntent client-side; never touches our server.
3. **Webhook outcome**: Stripe POSTs `payment_intent.succeeded` (or `.payment_failed`) to `/webhook/payment`. Server verifies signature, looks up order by `payment_intent_id`, transitions `awaiting_payment → paid` or `→ failed`. Saga handles `failed`.
4. **Expiry path** (separate sub-demo): client never pays. Reservation sweeper (mirrors A5 watchdog) scans `awaiting_payment` past `reserved_until`, transitions to `expired`, reverts Redis inventory via existing `revert.lua`. Inventory becomes available again to other users.
5. **Observability**: Grafana dashboard updates live during the demo — `awaiting_payment_orders` gauge spikes then drops, webhook latency histogram, expiry-sweep counter. (Requires CP9 to be done first.)

### Why Pattern A (recap from backlog §13.1)

- Stripe + Ticketmaster + KKTIX are the demo-target audiences. Pattern A matches their architecture exactly.
- Solves the deeper `OrderStatusFailed` semantic gap (business decline vs service failure) — webhook payload classifies; user retries with new card; only service-side failures hit the saga.
- Real demo recordable end-to-end with mock-gateway-or-Stripe-test-mode without exposing real money.
- Senior interview talking point: "I refactored the payment integration twice — first an internal-saga model for the engineering exercise, then a webhook-receiver model when I matured the product." Shows architectural growth.

### Out-of-repo decisions

- **Frontend**: Add a thin `web/` workspace (Next.js + Stripe Elements). Optional for the README portfolio but required for the demo video. Keep in repo to keep the demo reproducible.
- **Stripe vs MockGateway**: ship with `MockStripeAdapter` (signed-webhook simulator) so the demo runs without Stripe credentials. Document the adapter swap in §6.x of PROJECT_SPEC. Real-Stripe instructions in a separate `docs/runbooks/stripe-integration.md` (CP5).

## Deferred / explicitly NOT in this roadmap

- **DLQ Worker** (formerly listed as next phase) — not blocking demo; defer until production traffic exists. Current DLQ + watchdog cover the recovery surface.
- **Event Sourcing / CQRS** — over-engineered for portfolio scope. `order_status_history` + outbox give us most of the audit value at fraction of the complexity.
- **Real payment gateway integration** beyond MockStripeAdapter — deferred until there is genuine business need. Adapter pattern keeps the door open.
- **Bulkhead pattern** + **feature flags** — see [memory: post_roadmap_pr_plan §6](../.claude/projects/-Users-lileon-project-booking-monitor/memory/post_roadmap_pr_plan.md). No business signal yet to justify either.

## References

- [Phase 2 checkpoint review](checkpoints/20260430-phase2-review.md) — canonical findings + action plan
- [Architectural backlog §13](#) — payment-failed semantic refactor + Pattern A roadmap (in agent memory)
- [Scaling roadmap](scaling_roadmap.md) — historical Stage 1–4 architecture narrative
- [`PROJECT_SPEC.md` §6.7](PROJECT_SPEC.md) — Charging two-phase intent log + reconciler design
- [`PROJECT_SPEC.md` §6.7.1](PROJECT_SPEC.md) — Saga watchdog design (asymmetry note)
