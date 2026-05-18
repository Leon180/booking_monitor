// Package main is the entry for `cmd/booking-cli-stage5` — D12
// Stage 5 of the comparison harness, added 2026-05-13.
//
// Stage 5 = Redis Lua atomic deduct (NO XADD) + **Kafka durable
// queue** (acks=all) + async Kafka consumer worker + sync PG INSERT
// + Lua revert on Kafka publish failure + inventory drift detector:
//
//	API → EVAL deduct_no_xadd.lua (atomic DECRBY + HMGET)
//	    → IntakePublisher.PublishIntake to Kafka topic `booking.intake.v5`
//	      with RequireAll (acks=all), blocks until broker confirms
//	      replication
//	    → on publish FAIL: call revert.lua to re-add inventory, return 5xx
//	    → on publish SUCCESS: return 202 Accepted
//	    → worker: consume `booking.intake.v5` → INSERT orders → ACK offset
//	    → drift detector: every 30s, compare Redis qty vs DB awaiting+paid
//	      counts, alert on discrepancy (detection only — no auto-correct)
//
// Compared to Stage 3-4 (Redis Stream as handler→worker queue):
// Stage 5 moves the durable boundary from ephemeral Redis Stream to
// replicated Kafka. The trade-off: hot-path latency +1-5ms for the
// Kafka acks=all roundtrip, plus dual-write race needing reconciler.
// In return: Redis crash no longer loses in-flight bookings (the
// ghost-202 scenario documented in deploy/redis/redis.conf:59-65).
//
// Industry alignment:
//   - Damai (Alibaba subsidiary): SpringCloud + Kafka + Redis stack;
//     Stage 5 mirrors this architecture (with reconciler for the
//     dual-write race that RocketMQ tx messages would otherwise
//     handle natively).
//   - 12306 China rail: same withholding inventory + reserved_until
//     pattern we already have, with MQ async order generation.
//   - SeatGeek: PG WAL + Debezium variant; Stage 5 picks Kafka
//     directly instead.
//
// See docs/d12/README.md for the per-stage architecture matrix and
// docs/blog/2026-05-ticketing-vs-fintech.md for the design rationale.
//
// Subcommand layout (mirrors Stages 1-4):
//
//	server.go        — `server` subcommand: HTTP + Redis client +
//	                   IntakePublisher + Kafka consumer worker +
//	                   stage5Compensator + expiry sweeper +
//	                   drift detector.
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
	rootCmd := &cobra.Command{Use: "booking-cli-stage5"}

	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Stage 5 (Redis Lua + Kafka durable intake + drift reconciler) API server",
		Run:   runServer,
	}

	rootCmd.AddCommand(serverCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// resolveConfigPath reads CONFIG_PATH, falling back to the repo
// default. Mirrors Stages 1-4.
func resolveConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(envConfigPath)); p != "" {
		return p
	}
	return defaultConfigPath
}
