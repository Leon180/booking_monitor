package main

import (
	"context"
	"fmt"
	stdlog "log"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"booking_monitor/internal/application/expiry"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// runExpirySweeper is the `expiry-sweeper` subcommand entry — the D6
// reservation expiry sweeper that transitions overdue
// `awaiting_payment` reservations to `expired` and emits
// `order.failed` so the saga compensator reverts the Redis-side
// inventory deduct. Mirrors `runSagaWatchdog` in shape: same two run
// modes (default loop + --once), same fx graph pattern, same SIGTERM
// semantics.
//
// Why a separate process from the saga watchdog (which already has a
// sweep loop):
//
//   - Different blast radius: sweeper-side bug deadlocking the
//     watchdog would block stuck-Failed compensation; sweeper-side
//     bug deadlocking the watchdog's saga consumer would block
//     order.failed delivery. Process isolation gives independent
//     failure domains.
//   - Different cadence: D6 runs every 30s by default (every tick of
//     "row past reserved_until but unswept" is a soft-locked Redis
//     seat); watchdog runs every 60s (compensator failures recover
//     more gracefully).
//   - Symmetry: same architectural pattern as recon + saga_watchdog,
//     so operators learn one pattern and apply it to all three
//     sweeper subcommands.
//
// Why this graph DOES NOT install inventory rehydrate (unlike
// saga_watchdog.go:75):
//
//   D6 doesn't touch Redis directly. The sweeper writes ONLY to
//   Postgres (Order via MarkExpired + Outbox via Create). The saga
//   compensator (which DOES need inventory rehydrate, and is wired
//   in saga_watchdog.go) consumes the resulting `order.failed`
//   event from a separate process. Skipping the rehydrate here keeps
//   D6's fx graph minimal and avoids a hard dependency on Redis
//   being reachable at sweeper startup — the sweeper can usefully
//   process expired reservations even during a transient Redis
//   outage (the outbox accepts the rows; saga catches up later).
func runExpirySweeper(cmd *cobra.Command, _ []string) {
	// `GetBool` returns an error only on a programmer mistake (the
	// flag isn't registered on this command). The saga-watchdog +
	// recon precedents discard it with `_`, but that pattern hides
	// flag-wiring regressions: a missing `Flags().Bool("once", ...)`
	// in main.go would silently default to loop mode. Surface the
	// error as fatal — the operator gets a clear message at startup
	// instead of an unexpected long-running process where they
	// expected a one-shot.
	once, err := cmd.Flags().GetBool("once")
	if err != nil {
		stdlog.Fatalf("expiry-sweeper: read --once flag (programmer regression — flag must be registered in main.go): %v", err)
	}

	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("expiry-sweeper: load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),
		fx.Provide(
			// Translate the wire-format infrastructure config into the
			// application-layer expiry.Config + provide the Prometheus-
			// backed Metrics adapter. CommonModule already provides the
			// OrderRepository (with tracing decorator) and UnitOfWork.
			bootstrap.NewExpiryConfig,
			bootstrap.NewPrometheusExpiryMetrics,
			expiry.NewSweeper,
		),
		fx.Invoke(func(lc fx.Lifecycle, sd fx.Shutdowner, s *expiry.Sweeper, l *mlog.Logger, c *config.Config) error {
			return installExpirySweeper(lc, sd, s, l, c, once)
		}),
	)
	app.Run()
}

// installExpirySweeper wires the sweeper into the fx lifecycle.
// Mirrors installSagaWatchdog precisely so the run-mode dichotomy
// (loop / --once) stays consistent across all three sweepers.
func installExpirySweeper(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	sweeper *expiry.Sweeper,
	logger *mlog.Logger,
	cfg *config.Config,
	once bool,
) error {
	tp, err := initTracer()
	if err != nil {
		return fmt.Errorf("installExpirySweeper: %w", err)
	}

	// Metrics listener at :9091 (Phase 2 checkpoint O3). Sweeper-
	// metrics (expiry_sweep_resolved_total, expiry_oldest_overdue_age_seconds,
	// etc.) are useful primarily in loop mode — --once typically
	// exits before Prometheus scrapes. Listener is harmless either
	// way; mirrors saga_watchdog.go:105.
	if err := bootstrap.InstallMetricsListener(lc, cfg, logger); err != nil {
		return fmt.Errorf("installExpirySweeper: metrics listener: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			if once {
				go runExpirySweeperOnce(runCtx, sweeper, logger, shutdowner)
				return nil
			}
			// Read the loop interval off the Sweeper itself, mirroring
			// recon / saga's pattern. Single source-of-truth per
			// sweeper for tunables.
			go runExpirySweeperLoop(runCtx, sweeper, logger, sweeper.SweepInterval())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.L().Info("Stopping expiry sweeper")
			cancel()
			shutdownErr := tp.Shutdown(ctx)
			if shutdownErr != nil {
				logger.L().Error("tracer shutdown error", tag.Error(shutdownErr))
			}
			_ = logger.Sync()
			return shutdownErr
		},
	})
	return nil
}

// runExpirySweeperOnce performs ONE sweep then triggers fx shutdown.
// k8s CronJob host: container exits, scheduler reaps. Exit code 0
// on success / graceful cancel; exit code 1 on real Sweep failure
// (see plan v4 §I — `--once` mode exits 1 on
// `CountOverdueAfterCutoff` failure so CI sees the observability
// blind even though already-resolved rows in this tick stay
// committed).
//
// `ctx.Err() == nil` guard mirrors runSagaWatchdogOnce: a Sweep
// returning ctx.Canceled during graceful shutdown is NOT a real
// failure; only surface non-shutdown errors as exit-1.
func runExpirySweeperOnce(
	ctx context.Context,
	s *expiry.Sweeper,
	logger *mlog.Logger,
	shutdowner fx.Shutdowner,
) {
	logger.L().Info("expiry-sweeper: starting one-shot sweep")
	if err := safeSweep(ctx, "once_expiry_sweeper", s.Sweep, logger); err != nil && ctx.Err() == nil {
		logger.L().Error("expiry-sweeper: sweep failed", tag.Error(err))
		_ = shutdowner.Shutdown(fx.ExitCode(1))
		return
	}
	logger.L().Info("expiry-sweeper: one-shot sweep complete, exiting")
	_ = shutdowner.Shutdown(fx.ExitCode(0))
}

// runExpirySweeperLoop runs Sweep on a time.Ticker until ctx is
// cancelled. Boot-time first sweep + log-and-continue on per-tick
// errors mirrors runSagaWatchdogLoop / runSweepLoop. The "continuing"
// log line is what plan v4 §I calls out as the loop-mode response to
// a `CountOverdueAfterCutoff` failure: gauges held at last-known-good,
// `expiry_find_expired_errors_total` already incremented inside
// `Sweep`, next tick retries naturally.
func runExpirySweeperLoop(
	ctx context.Context,
	s *expiry.Sweeper,
	logger *mlog.Logger,
	interval time.Duration,
) {
	logger.L().Info("expiry-sweeper: starting loop", mlog.Duration("interval", interval))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := safeSweep(ctx, "expiry_sweeper", s.Sweep, logger); err != nil && ctx.Err() == nil {
		logger.L().Error("expiry-sweeper: boot sweep failed (continuing)", tag.Error(err))
	}

	for {
		select {
		case <-ctx.Done():
			logger.L().Info("expiry-sweeper: loop exiting", tag.Error(ctx.Err()))
			return
		case <-ticker.C:
			if err := safeSweep(ctx, "expiry_sweeper", s.Sweep, logger); err != nil && ctx.Err() == nil {
				logger.L().Error("expiry-sweeper: sweep failed (continuing)", tag.Error(err))
			}
		}
	}
}
