# Detect, don't fix — how saga / watchdog / drift detector split responsibility

> 中文版本(主要):[2026-05-detect-but-dont-fix.zh-TW.md](2026-05-detect-but-dont-fix.zh-TW.md)

> **TL;DR.** Four safety layers: a saga compensator (event-driven) plus three periodic sweepers (saga watchdog / recon / drift detector). **None of them auto-correct state — they detect, name the failure mode, page the on-call**. Three reviewers each asked "shouldn't this also fix what it finds?" and the answer is no: auto-correction races with in-flight operations, hides underlying bugs, and erodes the operator's mental model of the system. This post covers what each layer catches, why we chose four layers (not two, not eight), and the design rule "responsibility is non-overlapping; no layer's failure contaminates another's".

Pairs with [PR #45 (recon)](https://github.com/Leon180/booking_monitor/pull/45), [PR #49 (saga watchdog)](https://github.com/Leon180/booking_monitor/pull/49), [PR #76 (drift detector)](https://github.com/Leon180/booking_monitor/pull/76). Series template: [`docs/blog/README.md`](README.md).

## Context

When something goes wrong in the booking pipeline, several recovery paths can fire:

```
Happy:    API → Lua → stream → worker → DB → outbox → Kafka → payment
Failure 1: payment returns fail   → saga compensator (event-driven)
Failure 2: saga didn't get event  → saga watchdog (periodic sweep)
Failure 3: worker crashed in Charging → recon (periodic sweep against gateway)
Failure 4: Redis vs DB drift      → drift detector (periodic compare)
```

Each layer maps to a real failure mode that has actually occurred:
- **saga compensator** is day-1 design
- **saga watchdog** came from PR #49 (Phase 2 A5): observed 24h-stuck-in-Failed orders that the compensator never recovered
- **recon** came from PR #45 (Phase 2 A4): observed worker crashes leaving orders Charging for >1h
- **drift detector** came from PR #76 (cache-truth roadmap): the FLUSHALL incident dropped 411 messages silently

Together they form the "failed but recoverable" safety net. A common explicit rule applies: **all four detect; none of them automatically correct**.

## Options Considered

### Option 1: Single layer (saga compensator only)
**DISPROVED.** Kafka messages can be lost (retention expiry, broker crash before sync, consumer offset corruption); the saga compensator can panic; `order.failed` may never be published. **Single-layer guarantees aren't enough.**

### Option 2: Auto-correcting multi-layer (detect AND fix)
**REJECTED**, three problems:
- Races against in-flight Lua deduct
- Hides underlying bugs — operator never learns saga compensation is desyncing in production
- Recovery actions are typically destructive (touching live cache, mutating order state); they should be operator-gated

### Option 3: Multi-layer + detect-but-don't-fix (chosen)
- 4 independent layers, each owning a distinct failure mode
- Any layer detecting an anomaly pages on-call; no automatic mutation
- Operator reads the metric/alert/runbook then triggers recovery manually

### Option 4: No safety net
**Philosophical joke**. "Just execute correctly", return 5xx on failure. Unacceptable for ticketing (a lost message = customer received 202 but no order was created); listed mainly to make "why four layers?" a visible question.

## Decision

Option 3, with responsibility cleanly split. Each layer owns one specific failure mode; its detection signal and recovery path are operator-gated.

### The four-layer responsibility map

| Layer | Trigger | Failure mode it catches | Metric / Alert | Recovery (operator-gated) |
| :-- | :-- | :-- | :-- | :-- |
| saga compensator | event-driven (`order.failed`) | payment failure → revert Redis + mark compensated | n/a (silent on success) | none — this layer handles it |
| saga watchdog | 60s sweep | stuck-in-Failed (saga didn't receive event / panicked / >24h) | `saga_stuck_failed_orders > 0 for 10m` | retry compensator; >24h gets `max_age_exceeded`, page on-call |
| recon | 120s sweep | stuck-in-Charging (worker crashed before charging→confirmed/failed) | `recon_stuck_charging_orders > 0 for 5m` | query gateway: Charged → confirm; Declined/NotFound → fail; >24h force-fail + page |
| drift detector | 60s sweep | Redis ↔ DB inventory inconsistency (post-FLUSHALL / saga desync / manual SetInventory) | `inventory_drift_detected_total{direction}` | direction-specific runbook branch — typically rehydrate or saga compensation |

### Why four layers, not two or eight

**The case for four**: each layer detects a different failure shape; merging them would hide one.
- **saga compensator** is happy-path-ish-async. Invisible when it works.
- **saga watchdog** is the "event never arrived" safety net. Without it, a Kafka message loss is unrecoverable.
- **recon** is the "state transition died mid-flight" safety net. Without it, a worker crashing during Charging never produces an `order.failed`.
- **drift detector** is the "DB and cache disagree" safety net. Independent of saga — it watches Redis-side inconsistency.

**Why not eight**: diminishing returns. Each additional layer = more maintenance + more alert noise. Four is the minimum-coverage set we converged on after observing about 90% of failure modes.

### But the saga compensator is an exception

Notice the saga compensator is **event-driven**, not sweep-driven. It fires when it receives `order.failed`. **Is it "auto-correction"?**

Technically yes; semantically no — it runs as part of the normal failure-path flow, not as an independent monitoring layer. From a contract standpoint, "client got 202 / 200 OK" includes saga compensation as part of the happy path's tail (for failed bookings). So it doesn't break the detect-but-don't-fix rule — it's not a detect layer at all; it's a happy-path handler.

## Result

### Quantitative

After PR-D landed, re-running the cache-truth smoke test (the original 411-message-loss case):

| Signal | Before | After |
| :-- | :-- | :-- |
| 411 message loss | unnoticed | `ConsumerGroupRecreated` alert within 60s + drift detector confirms `cache_missing` |
| Saga compensator broken | messages stuck until someone investigates | `saga_watchdog_resolved_total{outcome=compensator_error}` immediately visible |
| Worker crashed on Charging | orders stuck >1 hour | `recon_stuck_charging_orders` alert within 5m |
| Redis ↔ DB drift | customer sees sold_out while DB has tickets | `inventory_drift_detected_total{direction}` 60s-sweep detection |

Every layer's covered failure mode is empirically validated. **All four detect; none auto-corrects**.

### Industry alignment

"Detect-but-don't-fix multi-layer safety" is the industry consensus:

- **Stripe Idempotency Keys post**: idempotency store and charge engine are separate-responsibility. If the idempotency store fails, the charge engine doesn't auto-retry — it pages humans.
- **AWS reconciliation patterns**: reconciliation jobs detect differences and produce reports; **operator manually triggers fixes**.
- **Google SRE Book Chapter 22 (Cascading Failures)**: auto-recovery in the absence of well-defined invariants amplifies problems.
- **Shopify on auto-healing**: auto-healing is an anti-pattern when it means compromising observability.

The four-layer detect-but-don't-fix design lines up cleanly with this body of advice.

## Lessons

### 1. "Auto-healing" is a marketing term, not an engineering concept

Before this work I associated "self-healing system" with K8s + service mesh good-citizen behaviour. Working on this project changed my view: **95% of "self-healing" in distributed systems is "silently suppressing symptoms so the bug lives a few more days"**. Real self-healing (K8s pod restart, TCP retry) operates within well-defined invariants; cross-business-logic auto-correction usually doesn't fit that frame.

### 2. "Why doesn't this layer fix it?" is a good question with a clear answer

Three reviewers asked some version of it. The answer isn't "too hard"; it's "**fixing it would hide the underlying bug**". Phrased clearly, this is convincing; phrased poorly, it gets read as systemic laziness. Next time anyone asks the same question, I'll point them at this lesson.

### 3. Non-overlapping responsibility is a hard requirement

If saga watchdog and recon both monitored the same order's same failure mode, the operator would see two alerts firing simultaneously and not know which to trust. By design, each layer owns one failure mode — the `outcome` label / `direction` label keeps them visibly distinct. **The four layers have zero overlap.**

If a fifth layer is ever added (e.g., an outbox-relay watchdog), it must map to a previously-uncovered failure mode. Otherwise it's just splitting existing responsibility too finely and adding alert noise.

## What's next

Post 4 / 5 land when their evidence is ready: a Docker Mac NAT cap post once the O3.2 variant B bare-metal benchmark is in, or a Pattern A section-level sharding design post once the schema work has matured.

For Phase 3, the four-layer safety net **expands as Pattern A lands**:
- New reservation-expiry sweeper (handling `awaiting_payment` orders past TTL without payment) → fifth layer
- Saga compensator's scope narrows to `{expired, payment_failed}` (because the payment path becomes webhook-based; the happy path stops touching saga)

The reservation-expiry sweeper still follows detect-but-don't-fix: it detects orders where `reserved_until < now()`, then **operator-gated triggers revert** (add inventory back to Redis + mark expired). Same shape as recon, just a different trigger condition.

---

*If anything here is wrong or missing, please open an issue or PR against [`docs/blog/2026-05-detect-but-dont-fix.md`](https://github.com/Leon180/booking_monitor/blob/main/docs/blog/2026-05-detect-but-dont-fix.md). The blog is in-repo precisely so corrections go through the same review process as code.*
