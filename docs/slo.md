# SLI/SLO — booking_monitor

> PR 9 of the 8+1 PR CI/CD roadmap. Adds SLI/SLO definitions + multi-burn-rate alerts to upgrade the previous "production-LEAN" framing to legitimately "production-grade".

## What this document does

Defines the **service-level indicators** (numeric metrics) and **service-level objectives** (targets over windows) for booking_monitor. Encodes them as Prometheus [recording rules](../deploy/prometheus/recording_rules.yml) (pre-computed SLI ratios) + [SLO alerts](../deploy/prometheus/slo_alerts.yml) (multi-window/multi-burn-rate, per Google SRE Workbook ch. 5).

The companion document is [`docs/runbooks/chaos.md`](runbooks/chaos.md) — chaos engineering experiments that **verify the SLOs hold under failure injection**.

## SLI methodology (Google SRE)

Per the [SRE Workbook ch. 2 — Implementing SLOs](https://sre.google/workbook/implementing-slos/), an SLI is **"the rate at which valid events succeed."** The two-part definition matters:

- **valid events** — exclude events that aren't user-facing (e.g. health checks, internal metric scrapes). For our case: only `/api/v1/*` traffic counts.
- **succeed** — defined as `status != 5xx AND duration < threshold` for latency-sensitive endpoints.

## SLO targets

Three SLOs, all measured over a **30-day rolling window** (calendar-window SLOs introduce reset cliffs that mask sustained problems):

| Domain | SLI | SLO target | Error budget (30d) |
|---|---|---|---|
| **Availability** | `success_rate(api requests)` | **99.5%** of valid `/api/v1/*` requests succeed | 0.5% = **216 min/month** |
| **Latency (booking)** | `p99_latency(POST /api/v1/book)` | **99% of bookings respond < 500ms** | 1% = **432 min/month** |
| **Booking acceptance** | `accepted_bookings_per_second_under_load` | **≥ 500 acc/s sustained at 500 VUs for 60s** (per benchmark canonical scenario) | n/a — capacity SLO, not error-rate |

### Why these three

- **Availability** is the absolute baseline — without it nothing else matters
- **Booking latency p99** is the specific UX-critical path. Bookings within 500ms is the difference between "snappy" and "I clicked but did it work?". Aligned with PR 5's smoke probe response-time expectation.
- **Booking acceptance throughput** is the *capacity* SLO. Unlike error-rate SLOs, throughput SLOs are validated by **load tests** (see [PR 7 benchmarks](benchmarks/)), not by 24/7 monitoring. Documented here because it's a real user-facing promise (system is built for flash-sale traffic).

### What we DON'T track

- **Internal CPU / memory utilization** — these are saturation indicators (good for capacity planning) but **not** SLIs (user doesn't experience CPU%)
- **Redis hit rate** — internal optimization metric
- **Kafka consumer lag** — could become an SLI if we had a user-facing "guaranteed processing within N seconds" promise; we don't yet

## Error budget math

For each error-rate SLO, the error budget is:

```
error_budget_minutes = window_minutes × (1 - SLO)
```

Concrete:
- **Availability SLO 99.5% over 30 days** → `30 × 24 × 60 × 0.005 = 216 min/month`
- **Booking latency SLO 99% over 30 days** → `30 × 24 × 60 × 0.01 = 432 min/month`

The error budget **drives prioritization** (per SRE book ch. 3 — Embracing Risk):
- Budget healthy → ship new features
- Budget burning fast → freeze features, focus on reliability work
- Budget exhausted → reliability lockdown until next rolling-window resets the budget

## Burn rate alerting — the SRE Workbook ch. 5 pattern

We don't alert on "SLO already broken" because by then user pain is already happening. We alert on **burn rate** — the speed at which the error budget is being consumed.

**The 14.4x multiplier** (key derivation):
- 14.4x = "consuming 2% of 30-day error budget in 1 hour"
- At 14.4x burn rate continuously, the 30-day budget is exhausted in `30 ÷ 14.4 = 2.08 days` (~50 hours)
- Source: [SRE Workbook ch. 5 — Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/) Table 5-8

**Three alert tiers** per SRE Workbook Table 5-8:

| Burn rate | Long window | Short window | Budget consumed | Severity |
|---|---|---|---|---|
| **14.4x** | 1 hour | 5 minutes | 2% | **Page** (immediate response required) |
| **6x** | 6 hours | 30 minutes | 5% | **Page** (slower burn but still serious) |
| **1x** | 3 days | 6 hours | 10% | **Ticket** (creeping degradation; investigate next business day) |

**Why multi-window** (long + short, both must fire):
- Short window alone → false positives from brief bursts (a single retry storm triggers a 5m spike)
- Long window alone → too slow to catch sustained-but-acute problems (5m of 50% error rate gets averaged to 4% over 1h)
- Both required → catches sustained issues AND filters out one-off bursts

## Where it lives in code

### Recording rules

[`deploy/prometheus/recording_rules.yml`](../deploy/prometheus/recording_rules.yml) — hierarchical computation:

- **Tier 1** (5m windows, evaluated every 30s): cheap to compute, drives the short window of each alert
- **Tier 2** (30m / 6h windows, evaluated every 1m): aggregates Tier 1, drives the long window
- **Tier 3** (30d aggregation, evaluated every 5m): produces the error-budget-remaining ratio for the SLO dashboard

Hierarchical because `rate(http_requests_total[30d])` evaluated at the alert-eval frequency would scan 30 days of raw samples on every tick — expensive at scale. The pre-aggregation pattern is the [Prometheus rules best practices recommendation](https://prometheus.io/docs/practices/rules/).

### SLO alerts

[`deploy/prometheus/slo_alerts.yml`](../deploy/prometheus/slo_alerts.yml) — 3 tiers × 2 SLOs (availability + booking latency) = 6 alert rules. Each fires only when BOTH its long-window and short-window burn rate cross threshold.

### Dashboard

Not yet implemented — Grafana panels reading the recording rules. Tracked as a follow-up. The Prometheus-side wiring is the load-bearing part; the dashboard is the UI on top.

## Modern alternatives (not adopted in PR 9, documented for next-step)

- **[Sloth](https://sloth.dev/)** — generates Prometheus recording + alert rules from a YAML SLO spec. Cleaner than hand-writing rules; supports [OpenSLO](https://openslo.com/) (vendor-neutral SLO spec).
- **Grafana Cloud SLO** — managed SLO product. Adds a managed-service dependency we don't need at portfolio scale.

For booking_monitor PR 9 we hand-write the rules to keep the entire SLO stack visible + diffable in this repo. If we later migrate to a multi-service codebase, Sloth becomes the right answer.

## References

- [Google SRE Book ch. 4 — Service Level Objectives](https://sre.google/sre-book/service-level-objectives/)
- [Google SRE Workbook ch. 2 — Implementing SLOs](https://sre.google/workbook/implementing-slos/)
- [Google SRE Workbook ch. 5 — Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/)
- [Prometheus recording rules best practices](https://prometheus.io/docs/practices/rules/)
- [The Four Golden Signals — SRE Book ch. 6](https://sre.google/sre-book/monitoring-distributed-systems/)
- [Sloth SLO generator](https://sloth.dev/)
- [OpenSLO spec](https://openslo.com/)
