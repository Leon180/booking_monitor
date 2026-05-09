// Package main is the entry for `cmd/booking-cli-stage3` — D12
// Stage 3 of the 4-version comparison harness.
//
// Stage 3 = Redis Lua atomic deduct + orders:stream + async worker
// + sync PG INSERT (no Kafka, no outbox, no out-of-process saga):
//
//	API → EVAL deduct.lua (atomic DECRBY + HMGET + XADD orders:stream)
//	    → 202 Accepted (worker async-processes the stream message)
//	    → worker: BEGIN; DecrementTicket; INSERT orders; COMMIT
//
// Compared to Stage 2 (sync PG INSERT in the request path), Stage 3
// adds an async buffer between Redis and Postgres so the booking
// hot path doesn't pay the synchronous DB round-trip — the
// architectural trade-off the comparison harness measures.
//
// Compared to Stage 4 (current `cmd/booking-cli`), Stage 3 omits
// Kafka outbox + out-of-process saga consumer. Compensation
// (abandon TTL, /test/payment/confirm-failed) runs through an
// in-binary stage3Compensator + expiry sweeper goroutine — the
// same pattern Stages 1+2 use, augmented with revert.lua because
// Redis is the SoT for the live inventory counter.
//
// PG inventory column semantics (load-bearing — the rule a
// future contributor will most likely trip):
//
//   - Worker UoW DECREMENTS event_ticket_types.available_tickets
//     inside the same tx as INSERT orders. UoW rollback handles
//     the symmetric undo for any in-tx failure (e.g. 23505).
//   - stage3Compensator (out-of-band: sweeper + HandleTestConfirm
//     outcome=failed) MUST INCREMENT event_ticket_types.available_
//     tickets back, mirroring Stage 4's saga compensator. Forward
//     decrement in worker, backward increment in compensator —
//     symmetric SoT.
//   - This rule is OPPOSITE to Stage 2's (which leaves the column
//     untouched on both paths because Stage 2 has no worker
//     decrementing it).
//
// Subcommand layout (deliberately minimal vs cmd/booking-cli):
//
//	server.go   — `server` subcommand: HTTP + Redis client + worker +
//	              stage3Compensator + in-binary expiry sweeper. No
//	              outbox relay, no Kafka producer, no saga consumer.
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
	rootCmd := &cobra.Command{Use: "booking-cli-stage3"}

	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Stage 3 (Redis Lua + stream + async worker, no outbox) API server",
		Run:   runServer,
	}

	rootCmd.AddCommand(serverCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// resolveConfigPath reads CONFIG_PATH, falling back to the repo
// default. Mirrors Stages 1+2.
func resolveConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(envConfigPath)); p != "" {
		return p
	}
	return defaultConfigPath
}
