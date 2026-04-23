// Package bootstrap wires infrastructure-layer components that must
// know about both the domain-independent primitives (logger, config)
// and the application's specific configuration.
//
// Keeping these wiring modules here (rather than inside the primitive
// packages they wire) preserves the "pkg/ never depends on internal/"
// convention and makes the dependency graph tractable:
//
//	cmd/booking-cli/main.go
//	    ├── uses bootstrap.LogModule
//	    └── bootstrap  →  internal/log, internal/infrastructure/config
package bootstrap

import (
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/log"

	"go.uber.org/fx"
)

// LogModule provides a *log.Logger constructed from cfg.App.LogLevel.
// Replaces the old pkg/logger fx.Module which depended on the config
// package from inside pkg/ — a Go layout violation that made the
// logger package impossible to extract for reuse.
//
// Callers wire this module in their fx application (see
// cmd/booking-cli/main.go).
var LogModule = fx.Module("log",
	fx.Provide(func(cfg *config.Config) (*log.Logger, error) {
		lvl, err := log.ParseLevel(cfg.App.LogLevel)
		if err != nil {
			return nil, err
		}
		return log.New(log.Options{Level: lvl})
	}),
)
