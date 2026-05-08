// Package main is the entry for `cmd/booking-cli-stage1` — D12
// Stage 1 of the 4-version comparison harness.
//
// Stage 1 = pure synchronous baseline:
//
//	API → Postgres `BEGIN; SELECT FOR UPDATE; UPDATE event_ticket_types;
//	      INSERT orders; COMMIT`
//
// No Redis, no Kafka, no async worker, no out-of-process saga. The
// hot-path booking serializes on the row lock — that's the
// architectural cost the comparison harness will surface against
// Stages 2-4. See `docs/post_phase2_roadmap.md` D12 + the D12
// detailed plan for the full taxonomy.
//
// This file is intentionally thin: Cobra registration only. Logic
// lives in server.go (the `server` subcommand).
//
// Subcommand layout (deliberately minimal vs cmd/booking-cli):
//
//	server.go   — `server` subcommand: HTTP only; no worker, no
//	              outbox relay, no payment, no saga. The in-binary
//	              expiry sweeper goroutine for the abandon path
//	              lands in PR-D12.1 slice 5.
//
// Future stages (D12.2 / D12.3 / D12.4) live in their own
// `cmd/booking-cli-stage{2,3,4}/` directories. The current
// `cmd/booking-cli/` stays as Stage 4 unchanged (per the roadmap).
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const (
	apiV1Prefix = "/api/v1"

	envConfigPath     = "CONFIG_PATH"
	defaultConfigPath = "config/config.yml"
)

func main() {
	rootCmd := &cobra.Command{Use: "booking-cli-stage1"}

	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Stage 1 (sync SELECT FOR UPDATE) API server",
		Run:   runServer,
	}

	rootCmd.AddCommand(serverCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// resolveConfigPath reads CONFIG_PATH, falling back to the repo
// default. Same convention as cmd/booking-cli; lets us run under
// systemd / k8s initContainers where CWD differs.
func resolveConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(envConfigPath)); p != "" {
		return p
	}
	return defaultConfigPath
}
