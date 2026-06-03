package dora

import (
	"fmt"
	"strings"
	"time"
)

// RenderMarkdown returns the full `docs/dora.md` body. The format is
// designed for committed-to-repo + GitHub rendering — tables, no
// fancy charting, sparkline via Unicode block characters.
//
// The report is regenerated daily by the cron workflow. The git history
// of this file is itself a long-form trend artifact.
func RenderMarkdown(m *Metrics) string {
	var b strings.Builder
	writeHeader(&b, m)
	writeSummaryTable(&b, m)
	writeSparkline(&b, m)
	writeFooter(&b, m)
	return b.String()
}

func writeHeader(b *strings.Builder, m *Metrics) {
	fmt.Fprintf(b, "# DORA metrics — rolling %d days\n\n", m.WindowDays)
	fmt.Fprintf(b, "Auto-generated %s by [`booking-cli dora`](../cmd/booking-cli/dora.go). ", m.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(b, "See [docs/runbooks/dora.md](runbooks/dora.md) for the runbook + heuristic honesty notes.\n\n")
	fmt.Fprintf(b, "Per DORA 2024+ this report tracks **5 metrics** (FDRT replaces MTTR; Rework Rate added). ")
	fmt.Fprintf(b, "Per DORA 2025 the tier-based ranking model was retired — this report shows raw values + trend, not benchmarks.\n\n")
}

func writeSummaryTable(b *strings.Builder, m *Metrics) {
	fmt.Fprintln(b, "## Current metrics")
	fmt.Fprintln(b, "")
	fmt.Fprintln(b, "| Metric | Value | Window |")
	fmt.Fprintln(b, "| --- | --- | --- |")

	fmt.Fprintf(b, "| Deploys (total) | %d | %d days |\n", m.TotalDeploys, m.WindowDays)
	fmt.Fprintf(b, "| Deploys (successful) | %d | %d days |\n", m.SuccessfulDeploys, m.WindowDays)
	fmt.Fprintf(b, "| **Deployment Frequency** (median per day) | %s | %d days |\n",
		floatOrNA(m.DeploymentFrequencyPerDay, "%.2f /day"), m.WindowDays)
	fmt.Fprintf(b, "| **Lead Time for Changes** (p50 / p90 hours) | %s / %s | %d days |\n",
		floatOrNA(m.LeadTimeP50Hours, "%.1fh"),
		floatOrNA(m.LeadTimeP90Hours, "%.1fh"),
		m.WindowDays)
	fmt.Fprintf(b, "| **Failed Deployment Recovery Time** (p50 / p90 hours) | %s / %s | %d days |\n",
		floatOrNA(m.FDRTP50Hours, "%.1fh"),
		floatOrNA(m.FDRTP90Hours, "%.1fh"),
		m.WindowDays)
	fmt.Fprintf(b, "| **Change Failure Rate** | %s | %d days |\n",
		floatOrNA(m.ChangeFailureRate, "%.1f%%", 100),
		m.WindowDays)
	fmt.Fprintf(b, "| **Deployment Rework Rate** | %s | %d days |\n",
		floatOrNA(m.DeploymentReworkRate, "%.1f%%", 100),
		m.WindowDays)
	fmt.Fprintln(b, "")
}

func writeSparkline(b *strings.Builder, m *Metrics) {
	if len(m.Daily) == 0 {
		return
	}
	fmt.Fprintln(b, "## Daily deploys (sparkline)")
	fmt.Fprintln(b, "")
	fmt.Fprintf(b, "```\n")
	// Block characters by intensity.
	blocks := []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	maxN := 0
	for _, d := range m.Daily {
		if d.Total > maxN {
			maxN = d.Total
		}
	}
	if maxN == 0 {
		fmt.Fprintln(b, "(no deploys in window)")
		fmt.Fprintln(b, "```")
		return
	}
	// One char per day.
	var line strings.Builder
	for _, d := range m.Daily {
		idx := (d.Total * (len(blocks) - 1)) / maxN
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		line.WriteRune(blocks[idx])
	}
	fmt.Fprintln(b, line.String())
	fmt.Fprintf(b, "%s          %s\n",
		m.Daily[0].Date.Format("Jan 02"),
		m.Daily[len(m.Daily)-1].Date.Format("Jan 02"))
	fmt.Fprintln(b, "```")
	fmt.Fprintln(b, "")
}

func writeFooter(b *strings.Builder, m *Metrics) {
	fmt.Fprintln(b, "## Heuristic honesty notes")
	fmt.Fprintln(b, "")
	fmt.Fprintln(b, "These metrics are computed from public GitHub APIs (Deployments + Releases) without an explicit incident-tracking system. Several values are **approximations** — directional, not authoritative.")
	fmt.Fprintln(b, "")
	fmt.Fprintln(b, "- **Lead Time for Changes** is measured from `main` commit timestamp to successful deploy timestamp. Because we use squash-merge, the commit timestamp is the PR *merge* time, not the first-commit time. Real-world lead time including PR-in-flight period is likely 1–7 days longer.")
	fmt.Fprintln(b, "")
	fmt.Fprintln(b, "- **Change Failure Rate** counts deploys with `state=failure` + deploys following another deploy within 24 hours. The latter is a hotfix-implies-prior-deploy-was-bad heuristic. **Misses silent bugs** (deploys that succeed but cause customer-visible incidents we don't auto-detect).")
	fmt.Fprintln(b, "")
	fmt.Fprintln(b, "- **Failed Deployment Recovery Time** pairs each `state=failure` deploy with the next `state=success` in the same environment. Failures with no following success are excluded (would inflate the metric). If you see suspiciously low FDRT, check the deploy count — small denominator amplifies outliers.")
	fmt.Fprintln(b, "")
	fmt.Fprintln(b, "- **Deployment Rework Rate** counts releases whose tag is a patch bump (`vX.Y.(Z+1)`) landing within 7 days of the previous release. Approximates the DORA 2024 definition (\"fraction of releases requiring a hotfix\") but doesn't catch reworks that go un-tagged.")
	fmt.Fprintln(b, "")
	fmt.Fprintln(b, "For a portfolio project these approximations are appropriate. Production-grade DORA on a real-money product would wire incidents through a real incident-tracking system (PagerDuty, Linear `incident` label, Sentry release-health) and use that as the authoritative failure signal.")
	fmt.Fprintln(b, "")
	fmt.Fprintln(b, "Source data: GitHub Deployments API (`/repos/Leon180/booking_monitor/deployments`) + Releases API. See [internal/dora/metrics.go](../internal/dora/metrics.go) for the exact computation.")
}

// floatOrNA formats `*float64`. If nil, returns "n/a". If a `multiplier`
// is passed (variadic, optional), it's multiplied before formatting —
// used to turn 0.123 into 12.3% via multiplier=100.
func floatOrNA(v *float64, format string, multiplier ...float64) string {
	if v == nil {
		return "n/a"
	}
	value := *v
	if len(multiplier) > 0 {
		value *= multiplier[0]
	}
	return fmt.Sprintf(format, value)
}

// Unused: surface time format helpers later if we add per-section
// rendering.
var _ = time.Time{}
