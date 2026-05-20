package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"booking_monitor/internal/infrastructure/api/middleware"
	"booking_monitor/internal/infrastructure/config"
)

// runAdminToken is the `booking-cli admin-token` subcommand:
// mints a JWT HS256 token for the admin SSE endpoint. The token
// is signed with cfg.AdminStream.JWTSecret and includes the
// caller-supplied user identifier.
//
// Usage:
//
//	booking-cli admin-token --user ops-leon --ttl 30m
//
// Then in the dashboard URL:
//
//	GET /api/v1/admin/events/stream?token=<output>
//
// Security:
//   - Token is printed to stdout; pipe to a file or set as env var,
//     do not commit
//   - TTL is bounded by the server's ADMIN_STREAM_JWT_MAX_TTL (default
//     1h); requesting longer = server rejects at validation
//   - Different secrets per environment (set via .env per deploy)
//   - When secret rotates, all existing tokens become invalid
//     immediately on server restart
func runAdminToken(cmd *cobra.Command, _ []string) {
	user, _ := cmd.Flags().GetString("user")
	if user == "" {
		fmt.Fprintln(os.Stderr, "error: --user is required")
		os.Exit(1)
	}
	ttl, _ := cmd.Flags().GetDuration("ttl")
	if ttl <= 0 {
		fmt.Fprintln(os.Stderr, "error: --ttl must be positive (e.g., 30m, 1h)")
		os.Exit(1)
	}

	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load config: %v\n", err)
		os.Exit(1)
	}
	if cfg.AdminStream.JWTSecret == "" {
		fmt.Fprintln(os.Stderr,
			"error: ADMIN_STREAM_JWT_SECRET is empty in config — set it before minting tokens")
		os.Exit(1)
	}

	token, err := middleware.MintAdminJWT([]byte(cfg.AdminStream.JWTSecret), user, ttl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: mint token: %v\n", err)
		os.Exit(1)
	}

	// Print the token to stdout (caller pipes or copies)
	fmt.Println(token)

	// Stderr metadata so the user sees expiry without polluting stdout
	fmt.Fprintf(os.Stderr, "user:      %s\n", user)
	fmt.Fprintf(os.Stderr, "ttl:       %s\n", ttl)
	fmt.Fprintf(os.Stderr, "expires:   %s\n", time.Now().Add(ttl).Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "\nUse with: ?token=<token-above>\n")
}
