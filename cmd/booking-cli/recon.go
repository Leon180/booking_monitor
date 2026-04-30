package main

import (
	"context"
	"fmt"
	stdlog "log"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"booking_monitor/internal/application/recon"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	paymentInfra "booking_monitor/internal/infrastructure/payment"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// runRecon is the `recon` subcommand entry — the reconciler that
// resolves orders stuck in Charging by querying the payment gateway.
//
// Two run modes:
//
//   - Default: long-running ticker loop. Runs until SIGTERM. Suits
//     docker-compose / Deployment-style hosting.
//   - --once:  single sweep then exit. Suits k8s CronJob hosting where
//     the orchestrator handles the schedule. Same logic as one
//     iteration of the loop; same Sweep call underneath.
//
// Both modes wire the same fx graph (CommonModule + payment gateway +
// reconciler) so behaviour is identical between schedules.
func runRecon(cmd *cobra.Command, _ []string) {
	once, _ := cmd.Flags().GetBool("once")

	cfg, err := config.LoadConfig(resolveConfigPath())
	if err != nil {
		stdlog.Fatalf("recon: load config: %v", err)
	}

	app := fx.New(
		bootstrap.CommonModule(cfg),
		fx.Provide(
			// MockGateway implements both PaymentCharger and
			// PaymentStatusReader. The reconciler only needs the
			// reader half (defense against accidental Charge calls
			// from recon code) — fx.As scopes the injection to
			// just the read-side port.
			fx.Annotate(paymentInfra.NewMockGateway, fx.As(new(domain.PaymentStatusReader))),
			// Translate the wire-format infrastructure config into the
			// application-layer recon.Config + provide the Prometheus-
			// backed Metrics adapter. Closes the layer-violation
			// finding A1 (Phase 2 checkpoint): recon.NewReconciler now
			// depends only on application/domain types, not on
			// infrastructure/{config,observability}.
			bootstrap.NewReconConfig,
			bootstrap.NewPrometheusReconMetrics,
			recon.NewReconciler,
		),
		fx.Invoke(func(lc fx.Lifecycle, sd fx.Shutdowner, r *recon.Reconciler, l *mlog.Logger, c *config.Config) error {
			return installRecon(lc, sd, r, l, c, once)
		}),
	)
	app.Run()
}

// installRecon wires the reconciler into the fx lifecycle.
//
// Loop mode: a goroutine runs Sweep on a time.Ticker until the
// runCtx is cancelled. Errors from Sweep are logged but never abort
// the loop — the next tick gets a fresh chance. Only an unexpected
// fatal (e.g., the Reconciler's logger Sync panicking) escalates via
// fx.Shutdown.
//
// --once mode: invoke Sweep ONCE in OnStart, then call Shutdown ourselves
// so fx exits cleanly with code 0 on success or 1 on Sweep failure.
// Mirrors the k8s CronJob contract: container exits, scheduler reaps.
func installRecon(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	reconciler *recon.Reconciler,
	logger *mlog.Logger,
	cfg *config.Config,
	once bool,
) error {
	tp, err := initTracer()
	if err != nil {
		return fmt.Errorf("installRecon: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			if once {
				go runSweepOnce(runCtx, reconciler, logger, shutdowner)
				return nil
			}
			// Read the loop interval off the Reconciler itself rather
			// than re-deriving it from the infrastructure cfg here —
			// keeps the source-of-truth for "this Reconciler's tunables"
			// in one place (the application-layer recon.Config that
			// NewReconciler consumed).
			go runSweepLoop(runCtx, reconciler, logger, reconciler.SweepInterval())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.L().Info("Stopping reconciler")
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

// runSweepOnce performs ONE sweep then triggers fx shutdown. Used by
// --once mode (k8s CronJob host). Exit code reflects sweep success:
//   - 0 on Sweep returning nil
//   - 0 when the parent ctx is cancelled (graceful SIGTERM = succeeded
//     job, per k8s CronJob semantics — the scheduler will retry on
//     the next cron tick)
//   - 1 on real Sweep failure (DB error etc.; preserved via fx.ExitCode)
//
// We use the `ctx.Err() == nil` pattern rather than enumerating
// context.Canceled / context.DeadlineExceeded individually:
//
//   - If the parent ctx was cancelled → Sweep's error is rooted in
//     that cancellation → exit 0 (graceful)
//   - If the parent ctx is healthy but Sweep returned an error → real
//     failure → exit 1 (operator pages)
//
// This handles future context-wrapping scenarios without needing to
// enumerate every context.* sentinel.
func runSweepOnce(
	ctx context.Context,
	r *recon.Reconciler,
	logger *mlog.Logger,
	shutdowner fx.Shutdowner,
) {
	logger.L().Info("recon: starting one-shot sweep")
	if err := r.Sweep(ctx); err != nil && ctx.Err() == nil {
		logger.L().Error("recon: sweep failed", tag.Error(err))
		_ = shutdowner.Shutdown(fx.ExitCode(1))
		return
	}
	logger.L().Info("recon: one-shot sweep complete, exiting")
	_ = shutdowner.Shutdown(fx.ExitCode(0))
}

// runSweepLoop runs Sweep on a time.Ticker until ctx is cancelled.
// Per-cycle errors are logged but never abort the loop — the next
// tick is a fresh chance.
//
// First sweep runs IMMEDIATELY (not after the first interval) so
// docker-compose / Deployment hosts don't wait `SweepInterval` on
// boot before doing their first reconciliation pass. This also
// matches the canonical Sidekiq Reliable / Hangfire pattern: do the
// startup-recovery check at boot, then continue on schedule.
func runSweepLoop(
	ctx context.Context,
	r *recon.Reconciler,
	logger *mlog.Logger,
	interval time.Duration,
) {
	logger.L().Info("recon: starting loop", mlog.Duration("interval", interval))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Boot-time first sweep — runs immediately, not after the first
	// `interval`. Lets docker-compose / Deployment hosts do their
	// startup-recovery pass on boot rather than waiting `interval`
	// (potentially minutes) for the first reconciliation.
	//
	// Errors are logged but never abort the loop — the next tick
	// gets a fresh chance. Suppressed when the parent ctx is
	// cancelled (graceful SIGTERM); see runSweepOnce comment for
	// the `ctx.Err() == nil` rationale.
	if err := r.Sweep(ctx); err != nil && ctx.Err() == nil {
		logger.L().Error("recon: boot sweep failed (continuing)", tag.Error(err))
	}

	for {
		select {
		case <-ctx.Done():
			logger.L().Info("recon: loop exiting", tag.Error(ctx.Err()))
			return
		case <-ticker.C:
			if err := r.Sweep(ctx); err != nil && ctx.Err() == nil {
				logger.L().Error("recon: sweep failed (continuing)", tag.Error(err))
			}
		}
	}
}
