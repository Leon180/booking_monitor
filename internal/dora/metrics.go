package dora

import (
	"math"
	"sort"
	"time"
)

// computeMetrics turns raw GitHub API data into a Metrics struct.
// All time math uses UTC.
//
// Heuristics + honesty notes:
//
//   CFR (Change Failure Rate):
//     numerator = failure_state_deploys + patch_bump_within_24h_of_prev_deploy
//     denominator = total deploy attempts
//     Approximation: misses silent bugs (deploys that succeed but
//     cause customer-visible incidents we don't auto-detect).
//     Documented in the report footer.
//
//   FDRT (Failed Deployment Recovery Time):
//     For each failure-state deploy, pair with the next success-state
//     deploy in the same environment. Compute gap. Take p50 + p90.
//     If a failure has no following success in the window, it's
//     "unrecovered" and excluded from the metric (with a count
//     surfaced in the report).
//
//   Lead Time for Changes:
//     For each successful deploy, look up the commit's committer
//     timestamp + compute (deploy_time - commit_time). p50 + p90.
//     CAVEAT: squash-merge convention means commit timestamp is PR
//     merge time, not first-commit time. Underestimates real
//     lead time by N days of PR review. Documented in footer.
//
//   Deployment Rework Rate:
//     For each release (Z), find the previous release (P). If Z is
//     a patch bump (Z.patch == P.patch + 1) AND time(Z) - time(P)
//     <= 7 days, count Z as a rework. Total reworks / total releases.
//     Approximation: doesn't catch reworks that don't trigger a tag.

func computeMetrics(deploys []Deployment, releases []Release, leadtimeCommits map[string]Commit, windowDays int, now time.Time) *Metrics {
	since := now.AddDate(0, 0, -windowDays)
	m := &Metrics{
		WindowDays:  windowDays,
		GeneratedAt: now,
	}

	// 1. Counts
	m.TotalDeploys = len(deploys)
	for _, d := range deploys {
		if d.State == "success" {
			m.SuccessfulDeploys++
		}
	}

	// 2. Daily snapshots (for sparkline)
	m.Daily = bucketByDay(deploys, since, now)

	// 3. Deployment Frequency: p50 of daily successful-deploy counts
	if windowDays > 0 {
		freq := median(intsFromDaily(m.Daily, true))
		m.DeploymentFrequencyPerDay = floatPtr(freq)
	}

	// 4. Lead Time for Changes
	if leadtimes := collectLeadTimes(deploys, leadtimeCommits); len(leadtimes) > 0 {
		m.LeadTimeP50Hours = floatPtr(percentileHours(leadtimes, 50))
		m.LeadTimeP90Hours = floatPtr(percentileHours(leadtimes, 90))
	}

	// 5. FDRT
	if recoveries := collectRecoveries(deploys); len(recoveries) > 0 {
		m.FDRTP50Hours = floatPtr(percentileHours(recoveries, 50))
		m.FDRTP90Hours = floatPtr(percentileHours(recoveries, 90))
	}

	// 6. CFR
	if m.TotalDeploys > 0 {
		failures := countFailureSignals(deploys)
		cfr := float64(failures) / float64(m.TotalDeploys)
		m.ChangeFailureRate = floatPtr(cfr)
	}

	// 7. Deployment Rework Rate
	if len(releases) > 0 {
		rework := countReworkReleases(releases)
		rate := float64(rework) / float64(len(releases))
		m.DeploymentReworkRate = floatPtr(rate)
	}

	return m
}

// bucketByDay returns one entry per day in [since, now], with counts
// of deployments that landed that day. Zero-pads days with no deploys
// so the sparkline doesn't have gaps.
func bucketByDay(deploys []Deployment, since, now time.Time) []DailySnapshot {
	startDay := truncDay(since)
	endDay := truncDay(now)
	days := int(endDay.Sub(startDay).Hours()/24) + 1
	out := make([]DailySnapshot, days)
	for i := range out {
		out[i].Date = startDay.AddDate(0, 0, i)
	}
	for _, d := range deploys {
		if d.CreatedAt.Before(startDay) || d.CreatedAt.After(endDay.Add(24*time.Hour)) {
			continue
		}
		idx := int(truncDay(d.CreatedAt).Sub(startDay).Hours() / 24)
		if idx < 0 || idx >= days {
			continue
		}
		out[idx].Total++
		switch d.State {
		case "success":
			out[idx].Successful++
		case "failure", "error":
			out[idx].Failed++
		}
	}
	return out
}

// collectLeadTimes computes deploy_time - commit_time for each successful
// deploy whose SHA we have a Commit for. Skips deploys where the SHA
// lookup returned empty (rare — fork-only SHAs etc).
func collectLeadTimes(deploys []Deployment, commits map[string]Commit) []time.Duration {
	var out []time.Duration
	for _, d := range deploys {
		if d.State != "success" {
			continue
		}
		c, ok := commits[d.SHA]
		if !ok || c.CommitObj.Committer.Date.IsZero() {
			continue
		}
		gap := d.CreatedAt.Sub(c.CommitObj.Committer.Date)
		if gap <= 0 {
			continue // clock skew / data inconsistency
		}
		out = append(out, gap)
	}
	return out
}

// collectRecoveries pairs each failure-state deploy with the next
// success-state deploy in the same environment and returns the
// time-to-recover. Failures with no following success are excluded
// (logged in the report as "unrecovered failures").
func collectRecoveries(deploys []Deployment) []time.Duration {
	// Sort ASC by created_at to make the pairing pass linear.
	sorted := make([]Deployment, len(deploys))
	copy(sorted, deploys)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})
	var out []time.Duration
	for i := range sorted {
		if sorted[i].State != "failure" && sorted[i].State != "error" {
			continue
		}
		// Find next success in the same env after this failure.
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Environment != sorted[i].Environment {
				continue
			}
			if sorted[j].State == "success" {
				out = append(out, sorted[j].CreatedAt.Sub(sorted[i].CreatedAt))
				break
			}
		}
	}
	return out
}

// countFailureSignals approximates CFR per the documented heuristic.
// failure-state count + patch-bump-within-24h-of-prev signal.
func countFailureSignals(deploys []Deployment) int {
	// Sort ASC for the "within 24h" lookback.
	sorted := make([]Deployment, len(deploys))
	copy(sorted, deploys)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})
	count := 0
	for i := range sorted {
		if sorted[i].State == "failure" || sorted[i].State == "error" {
			count++
			continue
		}
		// "Patch deploy following a deploy in the last 24h" is the
		// hotfix-implies-prior-deploy-was-bad signal. We approximate
		// "patch deploy" as ANY deploy following another within 24h
		// — we don't have semver info on deploy SHAs without
		// matching tags, which would inflate the cost. Conservative
		// approximation; documented as such.
		if i > 0 && sorted[i].State == "success" && sorted[i-1].State == "success" {
			gap := sorted[i].CreatedAt.Sub(sorted[i-1].CreatedAt)
			if gap > 0 && gap < 24*time.Hour {
				// Within 24h follow-up of a prior success: arguably
				// a hotfix. But we don't double-count: each prior
				// success is "blamed" only by the immediately-next
				// deploy if within window. (Sliding pair window.)
				count++
			}
		}
	}
	return count
}

// countReworkReleases counts releases that look like quick hotfixes
// (patch bump within 7 days of the previous release).
//
// Pure approximation — we don't track real production incidents.
// Documented in the report footer.
func countReworkReleases(releases []Release) int {
	if len(releases) < 2 {
		return 0
	}
	// Sort ASC by published_at.
	sorted := make([]Release, len(releases))
	copy(sorted, releases)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PublishedAt.Before(sorted[j].PublishedAt)
	})
	count := 0
	for i := 1; i < len(sorted); i++ {
		prev := sorted[i-1]
		cur := sorted[i]
		if isPatchBumpOver(prev.TagName, cur.TagName) {
			gap := cur.PublishedAt.Sub(prev.PublishedAt)
			if gap > 0 && gap <= 7*24*time.Hour {
				count++
			}
		}
	}
	return count
}

// isPatchBumpOver returns true if cur is a patch bump of prev.
// Tags expected as "v1.2.3" / "v1.2.3-rc1". A pre-release suffix on
// cur doesn't qualify as a stable patch hotfix.
func isPatchBumpOver(prev, cur string) bool {
	pmaj, pmin, ppatch, ppre := parseSemver(prev)
	cmaj, cmin, cpatch, cpre := parseSemver(cur)
	if pmaj < 0 || cmaj < 0 {
		return false
	}
	if cpre != "" {
		return false // pre-release tag, not a real hotfix
	}
	_ = ppre
	return cmaj == pmaj && cmin == pmin && cpatch == ppatch+1
}

// parseSemver returns (major, minor, patch, prerelease) for a v-prefixed
// SemVer string. Returns -1 in major if unparseable.
func parseSemver(s string) (int, int, int, string) {
	if len(s) == 0 || s[0] != 'v' {
		return -1, -1, -1, ""
	}
	rest := s[1:]
	// Split off prerelease (-...) and build (+...) suffixes.
	pre := ""
	for i := range rest {
		if rest[i] == '-' || rest[i] == '+' {
			pre = rest[i:]
			rest = rest[:i]
			break
		}
	}
	maj, minor, patch := -1, -1, -1
	cur := 0
	idx := 0
	for i := 0; i <= len(rest); i++ {
		if i == len(rest) || rest[i] == '.' {
			n, ok := atoi(rest[cur:i])
			if !ok {
				return -1, -1, -1, ""
			}
			switch idx {
			case 0:
				maj = n
			case 1:
				minor = n
			case 2:
				patch = n
			}
			idx++
			cur = i + 1
		}
	}
	if idx != 3 {
		return -1, -1, -1, ""
	}
	return maj, minor, patch, pre
}

func atoi(s string) (int, bool) {
	if len(s) == 0 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func percentileHours(durations []time.Duration, p int) float64 {
	if len(durations) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(float64(len(sorted))*float64(p)/100)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx].Hours()
}

func median(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int, len(values))
	copy(sorted, values)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return float64(sorted[mid])
	}
	return float64(sorted[mid-1]+sorted[mid]) / 2
}

func intsFromDaily(daily []DailySnapshot, successOnly bool) []int {
	out := make([]int, len(daily))
	for i, d := range daily {
		if successOnly {
			out[i] = d.Successful
		} else {
			out[i] = d.Total
		}
	}
	return out
}

func truncDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func floatPtr(v float64) *float64 { return &v }
