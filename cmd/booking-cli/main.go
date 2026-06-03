package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Code-level constants. Deployer-tunable values live in config.Config
// (see cfg.Server / cfg.Postgres); the constants below are either program
// contracts (API version) or config bootstrap (can't live in config by
// definition).
const (
	// apiV1Prefix is the single source of truth for the versioned API
	// router group. server.go registers it; stress.go targets it; any
	// integration tool that talks to the API imports it from here to
	// avoid drift.
	apiV1Prefix = "/api/v1"

	// Config bootstrap — can't be in config by definition.
	envConfigPath     = "CONFIG_PATH"
	defaultConfigPath = "config/config.yml"
)

// Subcommand entrypoints (runServer / runStress / etc.) and helpers
// (initTracer, resolveSampler, build*/start*/shutdown*) live in
// sibling files. main.go is intentionally thin: cobra registration only.
//
//	main.go            — root command + subcommand registration + resolveConfigPath
//	server.go          — `server` subcommand: HTTP + workers + saga consumer (in-process)
//	recon.go           — `recon` subcommand: stuck-Charging reconciler (loop or --once)
//	saga_watchdog.go   — `saga-watchdog` subcommand: stuck-Failed sweeper (loop or --once)
//	expiry_sweeper.go  — `expiry-sweeper` subcommand: D6 reservation-expiry sweeper (loop or --once)
//	stress.go          — `stress` subcommand: one-shot load generator
//	tracer.go          — OTel tracer init shared by every subcommand
//
// D7 (2026-05-08) deleted the legacy `payment` subcommand (the A4 auto-charge
// path that consumed `order.created` and called gateway.Charge). Pattern A
// drives money movement through `/api/v1/orders/:id/pay` (D4) + the
// provider webhook (D5); `order.failed` saga events have only D5
// (`payment_failed`) and D6 (`expired`) as production emitters.
//
// DB pool + log + base observability wiring lives in internal/bootstrap
// (CommonModule) so it can be re-used by any new subcommand without
// copy-paste.
func main() {
	rootCmd := &cobra.Command{Use: "booking-cli"}

	serverCmd := &cobra.Command{Use: "server", Short: "Run the API server", Run: runServer}

	reconCmd := &cobra.Command{
		Use:   "recon",
		Short: "Run the order-status reconciler (sweeps stuck-Charging orders)",
		Run:   runRecon,
	}
	// --once: single sweep then exit, suitable for k8s CronJob hosts.
	// Default (loop) is suitable for docker-compose / Deployment hosts.
	reconCmd.Flags().Bool("once", false, "Run a single sweep then exit (for k8s CronJob hosting)")

	sagaWatchdogCmd := &cobra.Command{
		Use:   "saga-watchdog",
		Short: "Run the saga watchdog (sweeps stuck-Failed orders, re-drives the compensator)",
		Run:   runSagaWatchdog,
	}
	// Same --once / loop semantics as recon — symmetry simplifies the
	// operator mental model.
	sagaWatchdogCmd.Flags().Bool("once", false, "Run a single sweep then exit (for k8s CronJob hosting)")

	expirySweeperCmd := &cobra.Command{
		Use:   "expiry-sweeper",
		Short: "Run the D6 reservation expiry sweeper (transitions overdue awaiting_payment → expired, emits order.failed for saga compensation)",
		Run:   runExpirySweeper,
	}
	// Same --once / loop semantics as recon + saga-watchdog.
	expirySweeperCmd.Flags().Bool("once", false, "Run a single sweep then exit (for k8s CronJob hosting)")

	stressCmd := &cobra.Command{Use: "stress", Short: "Run stress test", Run: runStress}
	stressCmd.Flags().IntP("concurrency", "c", 1000, "Concurrency level")
	stressCmd.Flags().IntP("requests", "n", 2000, "Total requests")
	stressCmd.Flags().String("base-url", stressDefaultBaseURL, "Target base URL (scheme://host:port)")
	stressCmd.Flags().String("ticket-type-id", "", "TicketType UUID (v7) to book against — required, obtain from `ticket_types[0].id` in the POST /api/v1/events response (D4.1+)")
	stressCmd.Flags().Int("user-range", stressDefaultUserRangeMax, "Upper bound for random user_id")

	adminTokenCmd := &cobra.Command{
		Use:   "admin-token",
		Short: "Mint a JWT for the admin SSE endpoint (?token=<jwt>)",
		Run:   runAdminToken,
	}
	adminTokenCmd.Flags().String("user", "", "Ops user identifier (e.g., ops-leon) — required")
	adminTokenCmd.Flags().Duration("ttl", 30*time.Minute, "Token lifetime (must be ≤ server's ADMIN_STREAM_JWT_MAX_TTL)")

	doraCmd := &cobra.Command{
		Use:   "dora",
		Short: "Generate the DORA metrics report (docs/dora.md) from GitHub APIs",
		Long: `Pulls deployments + releases + commits from the GitHub REST API,
computes the 5 DORA metrics (Deployment Frequency, Lead Time for Changes,
Failed Deployment Recovery Time, Change Failure Rate, Deployment Rework
Rate), and writes the rendered markdown to --output.

Driven by .github/workflows/dora.yml on daily cron + commit-back-to-repo.
Locally: GITHUB_TOKEN=$(gh auth token) booking-cli dora --dry-run`,
		Run: runDora,
	}
	doraCmd.Flags().String("output", "docs/dora.md", "Path to write the markdown report")
	doraCmd.Flags().Int("days", 90, "Rolling window in days")
	doraCmd.Flags().String("repo", "Leon180/booking_monitor", "GitHub repo in owner/name form")
	doraCmd.Flags().Bool("dry-run", false, "Print report to stdout instead of writing the file")

	rootCmd.AddCommand(serverCmd, stressCmd, reconCmd, sagaWatchdogCmd, expirySweeperCmd, adminTokenCmd, doraCmd, newVersionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// resolveConfigPath reads CONFIG_PATH, falling back to the repo default.
// The env var lets us run under systemd / k8s initContainers where CWD differs.
func resolveConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(envConfigPath)); p != "" {
		return p
	}
	return defaultConfigPath
}
