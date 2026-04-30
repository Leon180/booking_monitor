package main

import (
	"context"
	"fmt"
	stdlog "log"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/saga"
	"booking_monitor/internal/bootstrap"
	cacheinfra "booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// runSagaWatchdog is the `saga-watchdog` subcommand entry — the saga
// watchdog that re-drives orders stuck in Failed by re-invoking the
// existing (idempotent) compensator. Mirrors `runRecon` exactly in
// shape: same two run modes (default loop + --once), same fx graph
// pattern, same SIGTERM semantics. The difference is the dependency
// graph — watchdog needs the compensator + InventoryRepository (for
// the compensator's internal Redis revert), not the payment gateway.
//
// Why a separate process from the saga consumer (which already has
// the compensator wired):
//
//   - A buggy watchdog must not deadlock the order.failed Kafka
//     pipeline. Process isolation gives independent failure domains.
//   - The watchdog can run on a different cadence (k8s CronJob every
//     N minutes) than the always-on consumer.
//   - Symmetry with `recon`: same architectural pattern, easy mental
//     model for operators.
func runSagaWatchdog(cmd *cobra.Command, _ []string) {
	once, _ := cmd.Flags().GetBool("once")

	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("saga-watchdog: load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),
		// CommonModule does NOT wire Redis or the inventory repository
		// (those are server-side / consumer-side dependencies). Provide
		// them here for the compensator's Redis revert path.
		fx.Provide(
			cacheinfra.NewRedisClient,
			cacheinfra.NewRedisInventoryRepository,
			application.NewSagaCompensator,
			// Translate the wire-format infrastructure config into the
			// application-layer saga.Config + provide the Prometheus-
			// backed Metrics adapter. Closes the layer-violation
			// finding A1 (Phase 2 checkpoint): saga.NewWatchdog now
			// depends only on application/domain types, not on
			// infrastructure/{config,observability}.
			bootstrap.NewSagaConfig,
			bootstrap.NewPrometheusSagaMetrics,
			saga.NewWatchdog,
		),
		fx.Invoke(func(lc fx.Lifecycle, sd fx.Shutdowner, w *saga.Watchdog, l *mlog.Logger, c *config.Config) error {
			return installSagaWatchdog(lc, sd, w, l, c, once)
		}),
	)
	app.Run()
}

// installSagaWatchdog wires the watchdog into the fx lifecycle.
// Mirrors installRecon precisely so the run-mode dichotomy
// (loop / --once) is consistent across both sweepers — operators can
// learn one pattern, apply it to both.
func installSagaWatchdog(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	watchdog *saga.Watchdog,
	logger *mlog.Logger,
	cfg *config.Config,
	once bool,
) error {
	tp, err := initTracer()
	if err != nil {
		return fmt.Errorf("installSagaWatchdog: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			if once {
				go runSagaWatchdogOnce(runCtx, watchdog, logger, shutdowner)
				return nil
			}
			// Read the loop interval off the Watchdog itself, mirroring
			// the recon side's `reconciler.SweepInterval()` shape.
			// Single source-of-truth per sweeper for tunables.
			go runSagaWatchdogLoop(runCtx, watchdog, logger, watchdog.WatchdogInterval())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.L().Info("Stopping saga watchdog")
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

// runSagaWatchdogOnce performs ONE sweep then triggers fx shutdown.
// k8s CronJob host: container exits, scheduler reaps. Exit code 0
// on success / graceful cancel; exit code 1 on real Sweep failure.
// See runSweepOnce in recon.go for the `ctx.Err() == nil` rationale.
func runSagaWatchdogOnce(
	ctx context.Context,
	w *saga.Watchdog,
	logger *mlog.Logger,
	shutdowner fx.Shutdowner,
) {
	logger.L().Info("saga-watchdog: starting one-shot sweep")
	if err := w.Sweep(ctx); err != nil && ctx.Err() == nil {
		logger.L().Error("saga-watchdog: sweep failed", tag.Error(err))
		_ = shutdowner.Shutdown(fx.ExitCode(1))
		return
	}
	logger.L().Info("saga-watchdog: one-shot sweep complete, exiting")
	_ = shutdowner.Shutdown(fx.ExitCode(0))
}

// runSagaWatchdogLoop runs Sweep on a time.Ticker until ctx is
// cancelled. Same boot-time-first-sweep + log-and-continue pattern
// as runSweepLoop in recon.go.
func runSagaWatchdogLoop(
	ctx context.Context,
	w *saga.Watchdog,
	logger *mlog.Logger,
	interval time.Duration,
) {
	logger.L().Info("saga-watchdog: starting loop", mlog.Duration("interval", interval))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := w.Sweep(ctx); err != nil && ctx.Err() == nil {
		logger.L().Error("saga-watchdog: boot sweep failed (continuing)", tag.Error(err))
	}

	for {
		select {
		case <-ctx.Done():
			logger.L().Info("saga-watchdog: loop exiting", tag.Error(ctx.Err()))
			return
		case <-ticker.C:
			if err := w.Sweep(ctx); err != nil && ctx.Err() == nil {
				logger.L().Error("saga-watchdog: sweep failed (continuing)", tag.Error(err))
			}
		}
	}
}
