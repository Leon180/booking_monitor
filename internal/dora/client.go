package dora

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client is a minimal GitHub REST API client. We avoid the heavyweight
// google/go-github dep — the DORA report needs ~4 endpoints and that's
// the whole interaction surface. ~150 lines of hand-rolled HTTP costs
// less than a transitive dependency does.
type Client struct {
	httpClient *http.Client
	token      string
	owner      string
	repo       string
	baseURL    string // overridable for tests
}

// NewClient constructs a client against the public GitHub REST API.
// `ownerRepo` is in `owner/repo` form (the way `${{ github.repository }}`
// surfaces it). Pass the GH token via env.
func NewClient(ownerRepo, token string) (*Client, error) {
	owner, repo, ok := splitOwnerRepo(ownerRepo)
	if !ok {
		return nil, fmt.Errorf("dora: invalid repo %q, expected owner/repo", ownerRepo)
	}
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		token:      token,
		owner:      owner,
		repo:       repo,
		baseURL:    "https://api.github.com",
	}, nil
}

// ListDeployments returns ALL deployments in the environment with
// created_at >= since. GitHub's /deployments endpoint has no
// `since` parameter (verified 2026-05-31 via fact-check); we paginate
// with per_page=100 + client-side filter, stopping when results go
// older than `since`.
func (c *Client) ListDeployments(ctx context.Context, environment string, since time.Time) ([]Deployment, error) {
	var out []Deployment
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("environment", environment)
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		path := fmt.Sprintf("/repos/%s/%s/deployments?%s", c.owner, c.repo, q.Encode())

		var batch []Deployment
		if err := c.get(ctx, path, &batch); err != nil {
			return nil, fmt.Errorf("list deployments page %d: %w", page, err)
		}
		if len(batch) == 0 {
			break
		}
		// Filter + early-stop when we cross the `since` boundary.
		// /deployments returns DESCENDING by created_at so we can
		// stop the moment we hit the first too-old entry.
		hitOldBoundary := false
		for i := range batch {
			if batch[i].CreatedAt.Before(since) {
				hitOldBoundary = true
				break
			}
			out = append(out, batch[i])
		}
		if hitOldBoundary || len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// LatestStatus fetches the most recent /statuses entry for a
// deployment and returns its state (success / failure / etc.).
// GH /statuses returns DESC by created_at; we read element 0.
func (c *Client) LatestStatus(ctx context.Context, deploymentID int64) (DeploymentStatus, error) {
	path := fmt.Sprintf("/repos/%s/%s/deployments/%d/statuses?per_page=1", c.owner, c.repo, deploymentID)
	var statuses []DeploymentStatus
	if err := c.get(ctx, path, &statuses); err != nil {
		return DeploymentStatus{}, err
	}
	if len(statuses) == 0 {
		return DeploymentStatus{State: ""}, nil
	}
	return statuses[0], nil
}

// ListReleases returns all releases in the repo, paginated. We need
// the full list to compute Deployment Rework Rate (a hotfix patch
// landing within 7 days of a prior release).
func (c *Client) ListReleases(ctx context.Context, since time.Time) ([]Release, error) {
	var out []Release
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		path := fmt.Sprintf("/repos/%s/%s/releases?%s", c.owner, c.repo, q.Encode())

		var batch []Release
		if err := c.get(ctx, path, &batch); err != nil {
			return nil, fmt.Errorf("list releases page %d: %w", page, err)
		}
		if len(batch) == 0 {
			break
		}
		hitOldBoundary := false
		for i := range batch {
			if batch[i].PublishedAt.Before(since) {
				hitOldBoundary = true
				break
			}
			out = append(out, batch[i])
		}
		if hitOldBoundary || len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// GetCommit fetches a single commit's metadata. Used for Lead Time
// computation (`deploy_time - commit.committer.date`).
func (c *Client) GetCommit(ctx context.Context, sha string) (Commit, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", c.owner, c.repo, sha)
	var commit Commit
	if err := c.get(ctx, path, &commit); err != nil {
		return Commit{}, err
	}
	return commit, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// 404 is a normal "no data here" condition for some endpoints
		// (e.g., commits with no fork access). Empty out, no error.
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: %d: %s", path, resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func splitOwnerRepo(s string) (owner, repo string, ok bool) {
	for i := range s {
		if s[i] == '/' {
			return s[:i], s[i+1:], i > 0 && i < len(s)-1
		}
	}
	return "", "", false
}
