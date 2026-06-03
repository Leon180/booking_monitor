package dora

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixed reference times for deterministic tests
var (
	now = time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
)

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in           string
		wantMaj      int
		wantMin      int
		wantPatch    int
		wantPrelease string
	}{
		{"v1.2.3", 1, 2, 3, ""},
		{"v0.0.1", 0, 0, 1, ""},
		{"v10.20.30", 10, 20, 30, ""},
		{"v1.0.0-rc1", 1, 0, 0, "-rc1"},
		{"v1.0.0+build.5", 1, 0, 0, "+build.5"},
		{"v1.0.0-rc1+sha.abc", 1, 0, 0, "-rc1+sha.abc"},
		// negatives
		{"1.2.3", -1, -1, -1, ""},          // missing v
		{"v1.2", -1, -1, -1, ""},           // missing patch
		{"vfoo.bar.baz", -1, -1, -1, ""},   // non-numeric
		{"", -1, -1, -1, ""},               // empty
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			maj, minor, patch, pre := parseSemver(tc.in)
			assert.Equal(t, tc.wantMaj, maj)
			assert.Equal(t, tc.wantMin, minor)
			assert.Equal(t, tc.wantPatch, patch)
			assert.Equal(t, tc.wantPrelease, pre)
		})
	}
}

func TestIsPatchBumpOver(t *testing.T) {
	cases := []struct {
		prev, cur string
		want      bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.2.3", "v1.2.4", true},
		{"v1.0.0", "v1.0.2", false}, // skipped patch
		{"v1.0.0", "v1.1.0", false}, // minor bump
		{"v1.0.0", "v2.0.0", false}, // major bump
		{"v1.0.0", "v1.0.1-rc1", false}, // pre-release not "stable hotfix"
		{"v1.0.0-rc1", "v1.0.0", false}, // stable from pre-release isn't a patch bump
		{"junk", "v1.0.1", false},
		{"v1.0.0", "junk", false},
	}
	for _, tc := range cases {
		t.Run(tc.prev+"->"+tc.cur, func(t *testing.T) {
			assert.Equal(t, tc.want, isPatchBumpOver(tc.prev, tc.cur))
		})
	}
}

func TestPercentileHours(t *testing.T) {
	cases := []struct {
		name      string
		durations []time.Duration
		percentile int
		want       float64
	}{
		{"empty returns zero", nil, 50, 0},
		{"single value", []time.Duration{2 * time.Hour}, 50, 2.0},
		{
			"p50 of 1-5h",
			[]time.Duration{1 * time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 5 * time.Hour},
			50,
			3.0, // p50 of 5 samples is index ceil(5*0.5)-1 = 2 → 3h
		},
		{
			"p90 of 1-10h",
			[]time.Duration{
				1 * time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 5 * time.Hour,
				6 * time.Hour, 7 * time.Hour, 8 * time.Hour, 9 * time.Hour, 10 * time.Hour,
			},
			90,
			9.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := percentileHours(tc.durations, tc.percentile)
			assert.InDelta(t, tc.want, got, 0.01)
		})
	}
}

func TestComputeMetrics_HappyPath(t *testing.T) {
	// 4 deploys over 7 days: 3 success + 1 failure, then a recovery
	mkDeploy := func(daysAgo int, state, sha string) Deployment {
		return Deployment{
			SHA:         sha,
			Environment: "production",
			CreatedAt:   now.AddDate(0, 0, -daysAgo),
			State:       state,
		}
	}
	deploys := []Deployment{
		mkDeploy(6, "success", "aaa"),    // day -6
		mkDeploy(4, "failure", "bbb"),    // day -4 (failure)
		mkDeploy(3, "success", "ccc"),    // day -3 (recovery from -4)
		mkDeploy(1, "success", "ddd"),    // day -1
	}
	// Lead Time commits: each deploy's SHA points to a commit 2h earlier
	commits := map[string]Commit{
		"aaa": commitAt(now.AddDate(0, 0, -6).Add(-2 * time.Hour)),
		"ccc": commitAt(now.AddDate(0, 0, -3).Add(-2 * time.Hour)),
		"ddd": commitAt(now.AddDate(0, 0, -1).Add(-2 * time.Hour)),
	}
	// 2 releases: v1.0.0 6 days ago, v1.0.1 5 days ago (rework — within 7d)
	releases := []Release{
		{TagName: "v1.0.0", PublishedAt: now.AddDate(0, 0, -6)},
		{TagName: "v1.0.1", PublishedAt: now.AddDate(0, 0, -5)},
	}

	m := computeMetrics(deploys, releases, commits, 7, now)
	require.NotNil(t, m)

	assert.Equal(t, 7, m.WindowDays)
	assert.Equal(t, 4, m.TotalDeploys)
	assert.Equal(t, 3, m.SuccessfulDeploys)

	require.NotNil(t, m.LeadTimeP50Hours)
	assert.InDelta(t, 2.0, *m.LeadTimeP50Hours, 0.1)
	require.NotNil(t, m.LeadTimeP90Hours)
	assert.InDelta(t, 2.0, *m.LeadTimeP90Hours, 0.1)

	// FDRT: failure -4 → success -3 = 24 hours
	require.NotNil(t, m.FDRTP50Hours)
	assert.InDelta(t, 24.0, *m.FDRTP50Hours, 0.1)

	// CFR: 1 failure / 4 total = 25%
	require.NotNil(t, m.ChangeFailureRate)
	assert.InDelta(t, 0.25, *m.ChangeFailureRate, 0.01)

	// Rework Rate: 1 rework release (v1.0.1) / 2 total = 50%
	require.NotNil(t, m.DeploymentReworkRate)
	assert.InDelta(t, 0.5, *m.DeploymentReworkRate, 0.01)

	// Daily snapshot has 8 entries (day-6 to day-0 inclusive)
	assert.Len(t, m.Daily, 8)
}

func TestComputeMetrics_EmptyWindow(t *testing.T) {
	m := computeMetrics(nil, nil, nil, 30, now)
	require.NotNil(t, m)
	assert.Equal(t, 0, m.TotalDeploys)
	assert.Equal(t, 0, m.SuccessfulDeploys)
	assert.Nil(t, m.LeadTimeP50Hours)
	assert.Nil(t, m.LeadTimeP90Hours)
	assert.Nil(t, m.FDRTP50Hours)
	assert.Nil(t, m.ChangeFailureRate, "no deploys means no CFR")
	assert.Nil(t, m.DeploymentReworkRate, "no releases means no rework rate")
}

func TestComputeMetrics_CFR_HotfixWithin24h(t *testing.T) {
	// 2 successful deploys 4h apart → should count second as hotfix-implying
	mkDeploy := func(t time.Time, state, sha string) Deployment {
		return Deployment{
			SHA:         sha,
			Environment: "production",
			CreatedAt:   t,
			State:       state,
		}
	}
	deploys := []Deployment{
		mkDeploy(now.AddDate(0, 0, -3), "success", "aaa"),
		mkDeploy(now.AddDate(0, 0, -3).Add(4*time.Hour), "success", "bbb"),
		mkDeploy(now.AddDate(0, 0, -1), "success", "ccc"), // standalone, not a hotfix
	}
	m := computeMetrics(deploys, nil, nil, 7, now)
	require.NotNil(t, m.ChangeFailureRate)
	// 1 hotfix-implied / 3 total = 33.3%
	assert.InDelta(t, 1.0/3.0, *m.ChangeFailureRate, 0.01)
}

func TestRenderMarkdown_AllValuesPresent(t *testing.T) {
	freq := 1.5
	leadP50 := 24.0
	leadP90 := 72.0
	fdrtP50 := 8.0
	cfr := 0.1
	rework := 0.05
	m := &Metrics{
		WindowDays:                90,
		GeneratedAt:               now,
		TotalDeploys:              45,
		SuccessfulDeploys:         42,
		DeploymentFrequencyPerDay: &freq,
		LeadTimeP50Hours:          &leadP50,
		LeadTimeP90Hours:          &leadP90,
		FDRTP50Hours:              &fdrtP50,
		ChangeFailureRate:         &cfr,
		DeploymentReworkRate:      &rework,
		Daily:                     []DailySnapshot{{Date: now, Total: 1, Successful: 1}},
	}
	out := RenderMarkdown(m)
	assert.Contains(t, out, "5 metrics")
	assert.Contains(t, out, "1.50 /day") // DepFreq formatting
	assert.Contains(t, out, "24.0h / 72.0h") // Lead Time pair
	assert.Contains(t, out, "10.0%") // CFR
	assert.Contains(t, out, "5.0%") // Rework
	assert.Contains(t, out, "Heuristic honesty notes") // footer
	assert.NotContains(t, out, "Elite") // no tier rendering
}

func TestRenderMarkdown_MissingValues(t *testing.T) {
	m := &Metrics{
		WindowDays:        30,
		GeneratedAt:       now,
		TotalDeploys:      0,
		SuccessfulDeploys: 0,
		Daily:             []DailySnapshot{},
	}
	out := RenderMarkdown(m)
	assert.Contains(t, out, "n/a") // all nil values render as n/a
	assert.NotContains(t, out, "panic")
}

func commitAt(t time.Time) Commit {
	c := Commit{}
	c.CommitObj.Committer.Date = t
	return c
}
