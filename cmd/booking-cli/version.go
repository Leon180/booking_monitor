package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Build-time identity injected by `go build -ldflags="-X main.X=..."`
// from the Dockerfile (PR 2). Defaults are "unknown"/"dev" so a local
// `go run` without ldflags still produces a sensible printout.
//
// These are intentionally package-level vars (not const) so the linker's
// `-X` flag can rewrite them. Tested by `make docker-build && make docker-version-check`.
var (
	commit    = "unknown"
	version   = "dev"
	buildDate = "unknown"
)

// versionInfo returns a single-line build identity string. Exported (lowercase
// is fine — same package) so future Prometheus `app_info{...}` metric can
// reuse the same values without grepping ldflags.
func versionInfo() (commitHash, ver, built string) {
	return commit, version, buildDate
}

// newVersionCmd is wired into rootCmd by main.go alongside the other
// subcommands. Output is one line for grep-friendliness; multi-line
// would be richer but harder to consume in CI smoke checks.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build identity (commit, version, build date) and exit",
		Run: func(cmd *cobra.Command, args []string) {
			c, v, d := versionInfo()
			fmt.Printf("booking-cli version=%s commit=%s built=%s go=%s\n",
				v, c, d, runtimeVersion())
		},
	}
}

// runtimeVersion returns the Go toolchain version embedded in the binary
// via build info. Fallback string when unavailable (rare).
func runtimeVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.GoVersion
	}
	return "unknown"
}
