// Package dora computes the 5-metric DORA scorecard
// (Deployment Frequency, Lead Time for Changes, Failed Deployment
// Recovery Time, Change Failure Rate, Deployment Rework Rate) from
// the GitHub APIs and renders a markdown report.
//
// Per DORA 2024+ the previous 4-metric model expanded to 5 (split of
// MTTR → FDRT + addition of Deployment Rework Rate). Per DORA 2025
// the Elite/High/Medium/Low tier model was retired in favor of
// archetype-based interpretation. This package reflects the 2026
// state — no tier rendering, just trend + raw values.
//
// Source: https://dora.dev/guides/dora-metrics/
package dora

import "time"

// Deployment is the GH Deployments API view of a single deploy
// attempt. State is populated from the latest /statuses entry.
type Deployment struct {
	ID          int64     `json:"id"`
	SHA         string    `json:"sha"`
	Environment string    `json:"environment"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// State is one of: success, failure, in_progress, queued,
	// pending, error, inactive. Populated from /statuses[0].state
	// (the latest status). Empty if no statuses yet.
	State string `json:"-"`
}

// DeploymentStatus is the GH Deployments API state view.
type DeploymentStatus struct {
	ID          int64     `json:"id"`
	State       string    `json:"state"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// Release is the GH Releases API view.
type Release struct {
	TagName     string    `json:"tag_name"`
	CommitSHA   string    `json:"target_commitish"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
}

// Commit is the partial GH Commits API view we need (just the
// committer timestamp for Lead Time computation).
type Commit struct {
	SHA       string    `json:"sha"`
	CommitObj struct {
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

// Metrics is the full report data structure: 5 DORA metrics + the
// daily snapshots that drive the sparklines.
//
// All durations are stored in hours for easy table-rendering. nil
// pointer means "insufficient data to compute" — distinct from
// "0", which means "computed and the answer is zero".
type Metrics struct {
	// Window the metrics cover, e.g. "last 90 days"
	WindowDays  int
	GeneratedAt time.Time

	// Total deploy attempts to production in the window (any state).
	TotalDeploys int
	// Subset that landed: state=success.
	SuccessfulDeploys int

	// Deployment Frequency: deploys/day, p50 of daily counts in window.
	DeploymentFrequencyPerDay *float64

	// Lead Time for Changes: time from main-commit to successful
	// deploy, p50 + p90 (hours). nil if no successful deploys had a
	// commit lookup-able SHA.
	LeadTimeP50Hours *float64
	LeadTimeP90Hours *float64

	// Failed Deployment Recovery Time (renamed from MTTR in DORA
	// 2024): time from a failure-state deploy to the next
	// success-state deploy in the same environment.
	FDRTP50Hours *float64
	FDRTP90Hours *float64

	// Change Failure Rate: fraction of deploys requiring rework.
	// Heuristic: (failure_state + patch_bump_within_24h) / total.
	// nil if total=0.
	ChangeFailureRate *float64

	// Deployment Rework Rate (new in DORA 2024): fraction of releases
	// requiring a hotfix patch bump. Heuristic: patch_bump_within_7d
	// / total_releases.
	DeploymentReworkRate *float64

	// Daily snapshots over the window — for sparklines.
	Daily []DailySnapshot
}

// DailySnapshot is one day's deploys, used for the sparkline.
type DailySnapshot struct {
	Date              time.Time
	Total             int
	Successful        int
	Failed            int
}
