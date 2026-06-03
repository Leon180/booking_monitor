# DORA metrics runbook

How `booking-cli dora` + [`.github/workflows/dora.yml`](../../.github/workflows/dora.yml) compute the [DORA](https://dora.dev/) metrics for this repo, the heuristic approximations they use, and the data sources they touch.

## What this is + isn't

This is a **lightweight, portfolio-scale DORA dashboard**: a Go CLI that reads public GitHub APIs and writes a markdown report. There's no external service to maintain, no DB to query, no metrics endpoint to scrape. The data sources are:

- `GET /repos/Leon180/booking_monitor/deployments` — every CI deploy attempt (production environment). PR 5 wires this; PR 6 fixup moved Deployment-creation earlier so pre-verify failures get recorded too.
- `GET /repos/Leon180/booking_monitor/deployments/:id/statuses` — terminal state per deploy (success / failure / etc.).
- `GET /repos/Leon180/booking_monitor/releases` — every release-please-cut release (drives the Deployment Rework Rate).
- `GET /repos/Leon180/booking_monitor/commits/:sha` — committer timestamp per successful deploy's SHA (drives Lead Time).

What it ISN'T:
- **Not a production-grade DORA platform.** Real prod DORA wires incidents through an incident-tracking system (PagerDuty, Linear `incident` label, Sentry release-health) and uses that as the authoritative failure signal. We have no incident system → we approximate (see § Heuristic honesty below).
- **Not a real-time dashboard.** Cron-refreshed daily at 06:00 UTC. Acceptable because our release cadence is daily-to-weekly; an hourly refresh would burn GitHub API quota without changing the answer.

## The 5 DORA metrics (post-2024 expansion)

Per [DORA's 2024 metrics update](https://dora.dev/guides/dora-metrics/), the historical 4-metric set expanded:

| Group | Metric | What we compute |
| --- | --- | --- |
| Throughput | Deployment Frequency | Median of daily successful-deploy counts in window. |
| Throughput | Lead Time for Changes | p50 + p90 of `deploy_time - commit.committer.date` for each successful deploy. |
| Throughput | **Failed Deployment Recovery Time (FDRT)** — renamed from MTTR | For each failure-state deploy, pair with the next success-state deploy in the same environment. Compute gap. Take p50 + p90. |
| Stability | Change Failure Rate (CFR) | `(failure_state + hotfix_within_24h) / total_deploys`. |
| Stability | **Deployment Rework Rate** — new in 2024 | `releases_that_are_patch_bumps_within_7d / total_releases`. |

The DORA 2025 report **retired the Elite / High / Medium / Low tier model** in favor of 7 team archetypes blending delivery performance + human factors (burnout, friction). This report deliberately does NOT render tier badges — it shows raw values + a 90-day sparkline and lets you interpret the trend.

## Heuristic honesty notes (also in the dashboard footer)

These metrics are approximations. Several known limitations:

### Lead Time for Changes — squash-merge underestimate

Lead Time is `deploy_time - main_commit_time`. Because we use squash-merge, the commit timestamp on `main` is the PR *merge* time, not the first PR commit. Real-world lead time including PR-in-flight period is likely **1–7 days longer**.

Production-grade fix (not implemented in PR 7): query the GitHub PR API to find each merge SHA's parent PR, use `pr.created_at` instead of `commit.committer.date`. [Apache DevLake takes this approach](https://devlake.apache.org/docs/Metrics/LeadTime/). Tracked as a future enhancement.

### Change Failure Rate — misses silent bugs

CFR counts deploys with explicit `state=failure` **plus** deploys following another deploy within 24 hours (the hotfix-implies-prior-deploy-was-bad heuristic). What this **misses**:

- Deploys that succeeded but caused customer-visible incidents we don't auto-detect (no Sentry / PagerDuty integration)
- Deploys followed by a non-deploy hotfix (config change, DB rollback) without a fresh deploy

The 24-hour window choice is a compromise: too narrow misses slow-detected incidents, too wide picks up unrelated rapid deploys. 24h is the value most reference implementations use.

### Failed Deployment Recovery Time — small denominator amplifies noise

For each failure-state deploy, we pair with the next success-state deploy and compute the gap. Failures with **no following success in the window** are excluded (would skew the metric to "infinite recovery"). When you see a suspiciously low FDRT, check the failure count — at low volumes (e.g. 1 failure with a 30-minute recovery) the metric is high-variance and shouldn't be over-interpreted.

### Deployment Rework Rate — un-tagged reworks invisible

Counts releases where:
- The tag is a patch bump (`vX.Y.Z+1`)
- It landed within 7 days of the previous release

What this **misses**: hotfixes that didn't go through release-please (e.g. manual `git tag`, or a single-commit fix that bumped both the binary and the manifest but used a custom tagging path). For projects strictly using release-please (which we do post-PR 6), this should approach the true rework rate.

## Operational

### Refresh trigger

- **Auto**: daily 06:00 UTC via cron in [`.github/workflows/dora.yml`](../../.github/workflows/dora.yml)
- **Manual**: GitHub UI → Actions → DORA Refresh → "Run workflow"
- **Local**:
  ```bash
  GITHUB_TOKEN=$(gh auth token) ./bin/booking-cli dora --dry-run --days 30
  ```
  `--dry-run` prints to stdout instead of writing the file.

### Why daily-commit-back-to-repo

We chose `chore(dora): refresh metrics` daily commits over GitHub Pages because:
- The git history of `docs/dora.md` IS the long-form trend artifact — each day's snapshot is a commit.
- Zero new infrastructure (no Pages deploy, no scoping).
- Plays cleanly with release-please: `chore` commits are `hidden: true` in `release-please-config.json` → no release-PR pollution.

### Cost — daily commit pollution

The trade-off: every day produces a `chore(dora)` commit on main. Over a year that's ~365 commits. Acceptable because:
- They're hidden from CHANGELOG (release-please config).
- Each commit's diff is small (single file, the report).
- Provides a queryable history via `git log docs/dora.md` for trend analysis.

If commit-pollution becomes unacceptable, the alternative is a separate orphan branch (`dora-snapshots`) that's never merged into main. PR 7 didn't take this path to keep complexity low.

### When to rerun the workflow

- Major release lands and you want today's report immediately → manual trigger
- Cron failed (Sigstore Fulcio outage, GitHub API blip) → manual trigger
- Suspected wrong numbers → check the source data via `gh api`, then rerun

### When to manually edit `docs/dora.md`

Don't. The cron overwrites the entire file every refresh. Edits to add commentary/annotations should go in this runbook or as a separate report file.

## Related

- [PR 5 — deploy.yml + Deployment record creation](deploy.md)
- [PR 6 — release-please automation](release_pipeline.md)
- [DORA official docs](https://dora.dev/guides/dora-metrics/)
- [State of DevOps Report](https://cloud.google.com/devops/state-of-devops) (data underlying the metric definitions + trends)
