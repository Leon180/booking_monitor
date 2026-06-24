# DORA metrics — rolling 90 days

Auto-generated 2026-06-24 07:17 UTC by [`booking-cli dora`](../cmd/booking-cli/dora.go). See [docs/runbooks/dora.md](runbooks/dora.md) for the runbook + heuristic honesty notes.

Per DORA 2024+ this report tracks **5 metrics** (FDRT replaces MTTR; Rework Rate added). Per DORA 2025 the tier-based ranking model was retired — this report shows raw values + trend, not benchmarks.

## Current metrics

| Metric | Value | Window |
| --- | --- | --- |
| Deploys (total) | 4 | 90 days |
| Deploys (successful) | 0 | 90 days |
| **Deployment Frequency** (median per day) | 0.00 /day | 90 days |
| **Lead Time for Changes** (p50 / p90 hours) | n/a / n/a | 90 days |
| **Failed Deployment Recovery Time** (p50 / p90 hours) | n/a / n/a | 90 days |
| **Change Failure Rate** | 100.0% | 90 days |
| **Deployment Rework Rate** | 0.0% | 90 days |

## Daily deploys (sparkline)

```
                                                                  █                        
Mar 26          Jun 24
```

## Heuristic honesty notes

These metrics are computed from public GitHub APIs (Deployments + Releases) without an explicit incident-tracking system. Several values are **approximations** — directional, not authoritative.

- **Lead Time for Changes** is measured from `main` commit timestamp to successful deploy timestamp. Because we use squash-merge, the commit timestamp is the PR *merge* time, not the first-commit time. Real-world lead time including PR-in-flight period is likely 1–7 days longer.

- **Change Failure Rate** counts deploys with `state=failure` + deploys following another deploy within 24 hours. The latter is a hotfix-implies-prior-deploy-was-bad heuristic. **Misses silent bugs** (deploys that succeed but cause customer-visible incidents we don't auto-detect).

- **Failed Deployment Recovery Time** pairs each `state=failure` deploy with the next `state=success` in the same environment. Failures with no following success are excluded (would inflate the metric). If you see suspiciously low FDRT, check the deploy count — small denominator amplifies outliers.

- **Deployment Rework Rate** counts releases whose tag is a patch bump (`vX.Y.(Z+1)`) landing within 7 days of the previous release. Approximates the DORA 2024 definition ("fraction of releases requiring a hotfix") but doesn't catch reworks that go un-tagged.

For a portfolio project these approximations are appropriate. Production-grade DORA on a real-money product would wire incidents through a real incident-tracking system (PagerDuty, Linear `incident` label, Sentry release-health) and use that as the authoritative failure signal.

Source data: GitHub Deployments API (`/repos/Leon180/booking_monitor/deployments`) + Releases API. See [internal/dora/metrics.go](../internal/dora/metrics.go) for the exact computation.
