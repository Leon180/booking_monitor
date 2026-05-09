// Package main is the entry for `cmd/booking-cli-stage2` — D12
// Stage 2 of the 4-version comparison harness.
//
// Stage 2 = Redis-Lua atomic deduct + sync Postgres INSERT:
//
//	API → EVAL deduct_sync.lua (atomic DECRBY + metadata HMGET)
//	    → INSERT orders (single statement, no tx needed)
//	    → on INSERT failure: revert.lua against ticket_type_qty
//
// Inventory source-of-truth migrates from `event_ticket_types.
// available_tickets` (Stage 1's PG row) to `ticket_type_qty:{id}`
// in Redis. The architectural cost surfaced vs Stage 1 is the
// SoT migration; the architectural benefit is that the booking
// hot path doesn't hold a PG row lock for the duration of the
// INSERT — Lua serializes inside Redis instead. Stage 2's
// saturation point IS the synchronous PG INSERT itself; Stages 3-4
// add async buffering to push past it. See `docs/d12/README.md`
// Stage 2 section for the comparison-harness framing.
//
// This file stays intentionally thin: Cobra registration only.
// Logic lives in server.go.
//
// Subcommand layout (deliberately minimal vs cmd/booking-cli):
//
//	server.go   — `server` subcommand: HTTP + Redis client + sync
//	              booking service + Stage-2 compensator + in-binary
//	              expiry sweeper goroutine. No worker, no outbox
//	              relay, no Kafka, no out-of-process saga.
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
	rootCmd := &cobra.Command{Use: "booking-cli-stage2"}

	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Stage 2 (Redis Lua + sync PG INSERT) API server",
		Run:   runServer,
	}

	rootCmd.AddCommand(serverCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// resolveConfigPath reads CONFIG_PATH, falling back to the repo
// default. Mirrors Stage 1.
func resolveConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(envConfigPath)); p != "" {
		return p
	}
	return defaultConfigPath
}
