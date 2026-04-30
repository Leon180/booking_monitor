package main

import (
	"fmt"
	"os"
	"strings"

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

// Subcommand entrypoints (runServer / runPaymentWorker / runStress) and
// helpers (initTracer, resolveSampler, build*/start*/shutdown*) live in
// sibling files. main.go is intentionally thin: cobra registration only.
//
//	main.go     — root command + subcommand registration + resolveConfigPath
//	server.go   — `server` subcommand: HTTP + workers + saga consumer
//	payment.go  — `payment` subcommand: Kafka order.created consumer
//	recon.go    — `recon` subcommand: stuck-Charging reconciler (loop or --once)
//	stress.go   — `stress` subcommand: one-shot load generator
//	tracer.go   — OTel tracer init shared by server + payment + recon
//
// DB pool + log + base observability wiring lives in internal/bootstrap
// (CommonModule) so it can be re-used by any new subcommand without
// copy-paste.
func main() {
	rootCmd := &cobra.Command{Use: "booking-cli"}

	serverCmd := &cobra.Command{Use: "server", Short: "Run the API server", Run: runServer}
	paymentCmd := &cobra.Command{Use: "payment", Short: "Run the Payment Service worker", Run: runPaymentWorker}

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

	stressCmd := &cobra.Command{Use: "stress", Short: "Run stress test", Run: runStress}
	stressCmd.Flags().IntP("concurrency", "c", 1000, "Concurrency level")
	stressCmd.Flags().IntP("requests", "n", 2000, "Total requests")
	stressCmd.Flags().String("base-url", stressDefaultBaseURL, "Target base URL (scheme://host:port)")
	stressCmd.Flags().String("event-id", "", "Event UUID (v7) to book against — required, obtain via POST /api/v1/events")
	stressCmd.Flags().Int("user-range", stressDefaultUserRangeMax, "Upper bound for random user_id")

	rootCmd.AddCommand(serverCmd, stressCmd, paymentCmd, reconCmd, sagaWatchdogCmd)

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
