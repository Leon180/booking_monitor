package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"booking_monitor/internal/dora"

	"github.com/spf13/cobra"
)

// runDora is the `booking-cli dora` subcommand entry. Pulls DORA-shaped
// data from GitHub APIs + writes a markdown report. Driven by the daily
// cron in `.github/workflows/dora.yml`.
//
// Auth: reads GITHUB_TOKEN from env. In CI, the workflow's
// secrets.GITHUB_TOKEN supplies it. Local dev: `gh auth token` or
// a fine-grained PAT with `Contents: read` + `Deployments: read`.
func runDora(cmd *cobra.Command, _ []string) {
	output, _ := cmd.Flags().GetString("output")
	days, _ := cmd.Flags().GetInt("days")
	repo, _ := cmd.Flags().GetString("repo")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "DORA: GITHUB_TOKEN env var required")
		os.Exit(2)
	}

	service, err := dora.NewService(repo, token, days)
	if err != nil {
		fmt.Fprintf(os.Stderr, "DORA: invalid input: %v\n", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	metrics, err := service.Compute(ctx, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "DORA: compute: %v\n", err)
		os.Exit(3)
	}

	report := dora.RenderMarkdown(metrics)

	if dryRun {
		fmt.Print(report)
		return
	}
	// gosec G306: world-readable 0o644 would let any unprivileged
	// process on the CI runner read the file before it's committed.
	// 0o600 is gosec's recommended ceiling. The file is committed to
	// a public repo immediately after, so this restriction is purely
	// transit-time defense — but the lint rule is correct.
	if err := os.WriteFile(output, []byte(report), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "DORA: write %s: %v\n", output, err)
		os.Exit(4)
	}
	fmt.Fprintf(os.Stderr, "DORA: wrote %s (%d deploys / %d successful in last %d days)\n",
		output, metrics.TotalDeploys, metrics.SuccessfulDeploys, days)
}
