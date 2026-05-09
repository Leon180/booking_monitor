package main

import (
	"context"
	"fmt"
	stdlog "log"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"booking_monitor/internal/application/recon"
	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// safeSweep runs `sweep(ctx)` under a defer recover() so a panic
// inside Sweep doesn't kill the surrounding loop goroutine. The panic
// is converted to a returned error AND a labelled prometheus counter
// (`sweep_goroutine_panics_total{sweeper}`) — without the counter, a
// recovered panic in a long-lived loop is invisible to operators
// (process stays up, /metrics keeps serving last-known gauge values,
// `up{}` stays 1 → TargetDown does not fire).
//
// Why a helper rather than inlining defer recover() in each loop:
// the same shape is needed in runSweepLoop (recon) AND runDriftLoop
// (drift detector), and the same observability discipline (counter
// + ERROR log + return-as-error so the existing `err != nil &&
// ctx.Err() == nil` branch fires) applies to both.
func safeSweep(
	ctx context.Context,
	sweeper string,
	sweep func(context.Context) error,
	logger *mlog.Logger,
) (err error) {
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		// Capture the stack BEFORE doing anything else — debug.Stack()
		// must run inside the panicking goroutine's frame, and the
		// metric / log calls below could themselves throw if degraded.
		stack := debug.Stack()

		// Bump the counter first; even if logging fails below, the
		// alert path stays intact. WithLabelValues is internally
		// mutex-protected so this is safe from any goroutine.
		observability.RecordSweepPanic(sweeper)

		// Defensive nil-logger guard: a degraded fx wiring could
		// hand us a nil *mlog.Logger, which would itself panic
		// inside the recover() and ESCAPE this defer (Go semantics:
		// a panic during a deferred recover crashes the process).
		// Fall back to stdlog so the panic still leaves an audit
		// trail; the metric bump above is the truth signal anyway.
		if logger == nil {
			stdlog.Printf("%s: sweep goroutine recovered from panic: %v\n%s",
				sweeper, rec, stack)
		} else {
			logger.L().Error(sweeper+": sweep goroutine recovered from panic",
				tag.Error(fmt.Errorf("panic: %v", rec)),
				mlog.ByteString("stack", stack))
		}

		err = fmt.Errorf("%s: panic recovered: %v", sweeper, rec)
	}()
	return sweep(ctx)
}

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
			// D4.2: gateway adapter selected by `PAYMENT_PROVIDER` config
			// via shared `bootstrap.NewPaymentGateway` helper. Both
			// MockGateway and StripeGateway implement
			// `domain.PaymentGateway` (combined port);
			// fx.As scopes the injection to just the reader-half port
			// the reconciler consumes. (Pre-D7 there was a third
			// interface `PaymentCharger`; D7 deleted it along with the
			// legacy A4 auto-charge path.)
			fx.Annotate(bootstrap.NewPaymentGateway, fx.As(new(domain.PaymentStatusReader))),
			// Translate the wire-format infrastructure config into the
			// application-layer recon.Config + provide the Prometheus-
			// backed Metrics adapter. Closes the layer-violation
			// finding A1 (Phase 2 checkpoint): recon.NewReconciler now
			// depends only on application/domain types, not on
			// infrastructure/{config,observability}.
			bootstrap.NewReconConfig,
			bootstrap.NewPrometheusReconMetrics,
			recon.NewReconciler,
			// PR-D — inventory drift detector. Co-resident with the
			// reconciler in this same subcommand process: both are
			// "periodic Postgres-truth sweepers", and running them as
			// one process keeps the operational footprint small (one
			// CronJob / Deployment, one /metrics, one shared lifecycle).
			//
			// Drift detection needs Redis (read-only) — the recon
			// process previously didn't, so we wire JUST the client +
			// inventory repository here. Avoiding the full cache.Module
			// keeps the queue / idempotency / collectors out of recon's
			// graph; those belong with the booking server's lifecycle.
			cache.NewRedisClient,
			cache.NewRedisInventoryRepository,
			bootstrap.NewDriftConfig,
			bootstrap.NewPrometheusDriftMetrics,
			recon.NewInventoryDriftDetector,
		),
		fx.Invoke(func(lc fx.Lifecycle, sd fx.Shutdowner, r *recon.Reconciler, d *recon.InventoryDriftDetector, l *mlog.Logger, c *config.Config) error {
			return installRecon(lc, sd, r, d, l, c, once)
		}),
	)
	app.Run()
}

// installRecon wires the reconciler + the inventory drift detector
// into the fx lifecycle. Both run inside this same `recon` subcommand
// process — sibling sweepers with disjoint responsibilities (recon
// resolves stuck-Charging orders via gateway; drift detector compares
// Redis cache vs DB inventory). Sharing one process keeps operational
// footprint small (one CronJob / Deployment, one /metrics surface).
//
// Loop mode: two goroutines, one per sweeper, each running Sweep on
// its own time.Ticker until runCtx is cancelled. Errors from Sweep
// are logged but never abort the loop — the next tick gets a fresh
// chance. Only an unexpected fatal escalates via fx.Shutdown.
//
// --once mode: ALL sweepers fire ONCE in OnStart concurrently. fx
// shuts down after BOTH complete (whether successfully or not). Exit
// code reflects the worst outcome — any sweep failure → exit 1, all
// success → exit 0. Mirrors the k8s CronJob contract: container exits,
// scheduler reaps.
func installRecon(
	lc fx.Lifecycle,
	shutdowner fx.Shutdowner,
	reconciler *recon.Reconciler,
	driftDetector *recon.InventoryDriftDetector,
	logger *mlog.Logger,
	cfg *config.Config,
	once bool,
) error {
	tp, err := initTracer()
	if err != nil {
		return fmt.Errorf("installRecon: %w", err)
	}

	// Worker metrics listener (Phase 2 checkpoint O3). Without this
	// recon_* / inventory_drift_* metrics would stay dark — registered
	// into the recon process's default registry but unreachable to
	// Prometheus.
	if err := bootstrap.InstallMetricsListener(lc, cfg, logger); err != nil {
		return fmt.Errorf("installRecon: metrics listener: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			if once {
				// Both sweeps fire concurrently; the helper waits for
				// both before shutting down with the worst exit code.
				go runRecOnceBoth(runCtx, reconciler, driftDetector, logger, shutdowner)
				return nil
			}
			// Each sweeper reads its own interval off itself rather
			// than re-deriving from the infrastructure cfg here —
			// keeps the source-of-truth for tunables in one place
			// (the application-layer Config the constructor consumed).
			go runSweepLoop(runCtx, reconciler, logger, reconciler.SweepInterval())
			go runDriftLoop(runCtx, driftDetector, logger, driftDetector.SweepInterval())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.L().Info("Stopping reconciler + drift detector")
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

// runRecOnceBoth performs ONE sweep of BOTH the reconciler AND the
// drift detector concurrently, then triggers fx shutdown after both
// complete. Used by --once mode (k8s CronJob host). Exit code reflects
// the worst outcome:
//   - 0 if both Sweeps return nil (or were graceful-cancelled)
//   - 0 when the parent ctx is cancelled (graceful SIGTERM = succeeded
//     job, per k8s CronJob semantics — the scheduler will retry on the
//     next cron tick)
//   - 1 if either Sweep returned a real error (preserved via fx.ExitCode)
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
func runRecOnceBoth(
	ctx context.Context,
	r *recon.Reconciler,
	d *recon.InventoryDriftDetector,
	logger *mlog.Logger,
	shutdowner fx.Shutdowner,
) {
	logger.L().Info("recon + drift: starting one-shot sweep")

	// Run both sweeps concurrently. Each goroutine reports a single
	// boolean: did THIS sweep fail (real failure, parent ctx healthy)?
	//
	// `defer recover()` is mandatory: without it, a panic in either
	// Sweep skips the channel send and the receiver below blocks
	// forever. In a k8s CronJob host that means the pod hangs until
	// `activeDeadlineSeconds` reaps it — which can wedge a retry
	// storm if the panic is deterministic. The recover writes
	// `failed=true` so the surrounding logic still produces an
	// exit-1 and re-panics the value so the host runtime dumps the
	// stack instead of silencing it.
	reconFailed := make(chan bool, 1)
	driftFailed := make(chan bool, 1)

	go func() {
		err := safeSweep(ctx, "once_recon", r.Sweep, logger)
		failed := err != nil && ctx.Err() == nil
		if failed {
			logger.L().Error("recon: sweep failed", tag.Error(err))
		}
		reconFailed <- failed
	}()
	go func() {
		err := safeSweep(ctx, "once_drift", d.Sweep, logger)
		failed := err != nil && ctx.Err() == nil
		if failed {
			logger.L().Error("inventory drift: sweep failed", tag.Error(err))
		}
		driftFailed <- failed
	}()

	// Wait for both. Either failure → exit 1; both clean → exit 0.
	anyFailed := <-reconFailed
	if <-driftFailed {
		anyFailed = true
	}

	exitCode := 0
	if anyFailed {
		exitCode = 1
	}
	logger.L().Info("recon + drift: one-shot sweep complete, exiting",
		mlog.Int("exit_code", exitCode))
	_ = shutdowner.Shutdown(fx.ExitCode(exitCode))
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
	if err := safeSweep(ctx, "recon", r.Sweep, logger); err != nil && ctx.Err() == nil {
		logger.L().Error("recon: boot sweep failed (continuing)", tag.Error(err))
	}

	for {
		select {
		case <-ctx.Done():
			logger.L().Info("recon: loop exiting", tag.Error(ctx.Err()))
			return
		case <-ticker.C:
			if err := safeSweep(ctx, "recon", r.Sweep, logger); err != nil && ctx.Err() == nil {
				logger.L().Error("recon: sweep failed (continuing)", tag.Error(err))
			}
		}
	}
}

// runDriftLoop is the inventory-drift-detector counterpart of
// runSweepLoop — same boot-immediate-then-tick shape, same
// "log-and-continue on per-cycle failures" discipline. Separate
// goroutine so the two sweepers' tick cadences don't share a
// scheduler hot path; if the gateway-bound recon Sweep blocks for
// seconds, the drift detector still fires its checks on schedule.
func runDriftLoop(
	ctx context.Context,
	d *recon.InventoryDriftDetector,
	logger *mlog.Logger,
	interval time.Duration,
) {
	logger.L().Info("inventory drift: starting loop", mlog.Duration("interval", interval))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := safeSweep(ctx, "inventory_drift", d.Sweep, logger); err != nil && ctx.Err() == nil {
		logger.L().Error("inventory drift: boot sweep failed (continuing)", tag.Error(err))
	}

	for {
		select {
		case <-ctx.Done():
			logger.L().Info("inventory drift: loop exiting", tag.Error(ctx.Err()))
			return
		case <-ticker.C:
			if err := safeSweep(ctx, "inventory_drift", d.Sweep, logger); err != nil && ctx.Err() == nil {
				logger.L().Error("inventory drift: sweep failed (continuing)", tag.Error(err))
			}
		}
	}
}
