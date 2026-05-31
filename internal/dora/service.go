package dora

import (
	"context"
	"fmt"
	"time"
)

// Service orchestrates the metrics pipeline:
//   1. Pull deployments + releases from GitHub
//   2. Populate each deployment's State by fetching latest /statuses
//   3. Fetch the commit metadata for each successful deploy's SHA
//   4. Hand to computeMetrics()
type Service struct {
	client     *Client
	windowDays int
}

// NewService constructs a Service. `ownerRepo` is "owner/repo".
// `windowDays` is the rolling window for the report (e.g. 90).
func NewService(ownerRepo, token string, windowDays int) (*Service, error) {
	if windowDays <= 0 {
		return nil, fmt.Errorf("dora: windowDays must be positive, got %d", windowDays)
	}
	c, err := NewClient(ownerRepo, token)
	if err != nil {
		return nil, err
	}
	return &Service{client: c, windowDays: windowDays}, nil
}

// Compute is the orchestrator entry point. Returns a fully-populated
// Metrics ready to hand to RenderMarkdown().
func (s *Service) Compute(ctx context.Context, now time.Time) (*Metrics, error) {
	since := now.AddDate(0, 0, -s.windowDays)

	// 1. Deployments
	deploys, err := s.client.ListDeployments(ctx, "production", since)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}

	// 2. Latest status per deployment (parallelizable but sequential
	// here for simplicity — daily cron, ~30-100 deploys, fine).
	for i := range deploys {
		st, err := s.client.LatestStatus(ctx, deploys[i].ID)
		if err != nil {
			// Failures here are non-fatal — just log and continue
			// with empty State (deploy gets excluded from
			// success/failure counts). Worst case: undercounts.
			continue
		}
		deploys[i].State = st.State
	}

	// 3. Releases (for Rework Rate)
	releases, err := s.client.ListReleases(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}

	// 4. Commits — only for successful deploys (for Lead Time)
	commits := make(map[string]Commit, len(deploys))
	for _, d := range deploys {
		if d.State != "success" {
			continue
		}
		if _, already := commits[d.SHA]; already {
			continue
		}
		c, err := s.client.GetCommit(ctx, d.SHA)
		if err != nil {
			// Same non-fatal policy as statuses. Lead Time
			// gets fewer samples; documented in footer.
			continue
		}
		commits[d.SHA] = c
	}

	// 5. Compute
	return computeMetrics(deploys, releases, commits, s.windowDays, now), nil
}
