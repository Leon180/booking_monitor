# SLO Catalog (stub)

> Status: **STUB** (PR #129 A13). Candidate SLI definitions only — burn-rate alerts are NOT yet implemented. The full SLO + multi-window burn-rate alert work is documented as Phase 3 deferral in [`docs/architectural_backlog.md`](architectural_backlog.md#slo--multi-window-burn-rate-alerts).
>
> This file exists so the audit-trail question "does the project have an SLO doc?" can be answered "yes, here are the candidate SLIs and the deferral rationale" rather than "no, no document exists at all".

## Why this is a stub

Implementing the SRE Workbook Ch.5 multi-window burn-rate alert pattern requires:

1. **Recording rules** — pre-aggregated availability + latency series so the alert evaluator doesn't recompute the SLI every query.
2. **Six alert rules per SLO** — short-window-fast-burn + long-window-slow-burn × three severity windows (the canonical 1h/6h/3d × page/ticket layering).
3. **Error-budget panel additions** — Grafana panels showing burn rate vs error budget consumed for each SLI.
4. **Runbook entries per alert** — what to do when each burn-rate alert pages.

Estimated effort: ~2-3 days of focused work, primarily Prometheus + Grafana JSON, not Go code. Deferred to Phase 3 (production-shape demo) per the architectural-backlog deferral. The PR #129 audit landing this stub explicitly demotes the "implement SLO + burn-rate alerts" finding to NIT-deferred and treats the absence of any SLO document as the durable correctness gap.

## Candidate SLIs (not yet measured as SLOs)

These are the three SLIs the project would track once the SRE Workbook pattern is implemented. Numeric targets are placeholders — the methodology section below explains how to calibrate them against real production traffic.

### SLI-1 — Booking availability

- **Definition**: `sum(rate(http_requests_total{path="/api/v1/book",status=~"2..|409"}[5m])) / sum(rate(http_requests_total{path="/api/v1/book"}[5m]))`
- **Rationale**: 200 (booked) and 409 (sold-out / idempotent-duplicate) are both *successful* business outcomes from the booking endpoint's perspective — the load-shed gate worked. 4xx fingerprint mismatches and 5xx infrastructure failures are SLI failures.
- **Candidate target**: 99.5% (allows ~3.6h of degradation per month).
- **Current source-of-truth metric**: `http_requests_total` (path / status labels).

### SLI-2 — Pay endpoint p99 latency

- **Definition**: `histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{path="/api/v1/orders/:id/pay"}[5m])))`
- **Rationale**: `/pay` is the user-perceived checkout-completion path. p99 latency directly drives conversion drop-off; Stripe documents 800ms as the perceived-snappy threshold.
- **Candidate target**: p99 ≤ 500ms (industry convention for checkout flows).
- **Current source-of-truth metric**: `http_request_duration_seconds` histogram (path label).

### SLI-3 — Webhook ack p95

- **Definition**: `histogram_quantile(0.95, sum by (le) (rate(payment_webhook_duration_seconds_bucket[5m])))`
- **Rationale**: Stripe webhooks have a 30s timeout before they consider the endpoint unhealthy and back off. Sustained p95 above 5s puts the deployment on Stripe's degraded-listener list, which slows redeliveries for the entire account.
- **Candidate target**: p95 ≤ 2s.
- **Current source-of-truth metric**: `payment_webhook_duration_seconds` histogram (defined in `internal/infrastructure/observability/metrics_webhook.go`).

## Methodology when implementing

1. **Calibrate baseline** — capture two weeks of production traffic, compute current 95th-percentile + outage-rate envelope, set the SLO target a bit looser than the observed baseline so a stable system isn't immediately in error budget.
2. **Recording rules first** — add `availability:booking:ratio_rate5m` + analogous latency series to `deploy/prometheus/recording_rules.yml`. Implementation-side alerts evaluate the pre-aggregated series, not the underlying counters.
3. **Six alerts per SLI** — the SRE Workbook 1h/6h/3d × short/long windows for each. Page on the short-window-fast-burn pair; ticket on the long-window-slow-burn pair.
4. **Runbook per alert** — co-located in `docs/runbooks/` keyed off the alert name.

## References

- [Google SRE Workbook Ch.5 — Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/)
- [`docs/architectural_backlog.md` § SLO + multi-window burn-rate alerts](architectural_backlog.md#slo--multi-window-burn-rate-alerts) — the deferral rationale + revisit triggers.
- [`docs/monitoring.md`](monitoring.md) — current static-threshold alert catalog the burn-rate work eventually supersedes.
