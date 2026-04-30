// Package saga is the saga watchdog — a sweeper that re-drives
// orders stuck in OrderStatusFailed by re-invoking the existing
// (idempotent) compensator. Runs as its own `booking-cli saga-watchdog`
// subcommand, separate from the saga consumer process, so a buggy
// watchdog can't deadlock the order.failed Kafka pipeline.
//
// Design summary (full spec in docs/PROJECT_SPEC.md §A5):
//
//  1. Every sweep cycle:
//     - SELECT id, age FROM orders WHERE status='failed' AND
//       updated_at < NOW() - threshold ORDER BY updated_at ASC LIMIT batch
//       (uses the partial index from migration 000011)
//     - For each result, in its own per-order context:
//       - If age > MaxFailedAge → log + emit max_age_exceeded counter
//         (do NOT auto-transition — moving Failed → Compensated without
//         verifying inventory was reverted is unsafe; operator must
//         investigate)
//       - Else: build an OrderFailedEvent payload + invoke
//         compensator.HandleOrderFailed (which is idempotent via
//         OrderStatusCompensated check)
//  2. Exit cleanly on ctx.Done at loop boundaries.
//  3. Two run modes (cmd/booking-cli/saga_watchdog.go):
//     - Default loop: ticker-driven, runs until SIGTERM
//     - --once: single sweep then exit (k8s CronJob mode)
//
// Why re-drive the compensator vs republishing to Kafka:
//
//   - Direct call eliminates a Kafka round-trip + offset commit per
//     stuck order — fast and self-contained.
//   - The compensator's idempotency check (Status == Compensated)
//     handles the race where the saga consumer succeeds between our
//     FindStuckFailed query and our re-drive.
//   - Republishing would require knowing the original event_id +
//     correlation_id; reconstructing that from the order alone is
//     possible but adds complexity for no behaviour gain.
//   - If a future migration introduces ordering invariants on
//     order.failed (e.g., partition-key dependent), we'd revisit;
//     today the events are independent.
package saga

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// Watchdog holds the dependencies + tunables. Built once at process
// start and reused across every sweep cycle.
type Watchdog struct {
	orders      domain.OrderRepository
	compensator application.SagaCompensator
	cfg         config.SagaConfig
	log         *mlog.Logger
}

// NewWatchdog is the fx-friendly constructor. Decorates the logger
// with `component=saga_watchdog` so every log line is filterable in
// Loki/Grafana without per-call ceremony.
func NewWatchdog(
	orders domain.OrderRepository,
	compensator application.SagaCompensator,
	cfg *config.Config,
	logger *mlog.Logger,
) *Watchdog {
	return &Watchdog{
		orders:      orders,
		compensator: compensator,
		cfg:         cfg.Saga,
		log:         logger.With(mlog.String("component", "saga_watchdog")),
	}
}

// Sweep runs ONE watchdog cycle: scan for stuck-Failed orders,
// resolve each via the compensator, return. Both run modes (loop and
// --once) call this — the loop just calls it on a ticker, --once
// calls it once and exits. Single source of truth for sweep
// semantics.
//
// Returns the first non-recoverable error (FindStuckFailed query
// failure). Per-order failures (compensator error, GetByID race) are
// logged + counted but do NOT abort the sweep — we still want the
// remaining stuck orders resolved this cycle.
//
// ctx semantics: caller's ctx caps the WHOLE sweep. Each per-order
// resolve runs under its own derived ctx; on parent cancellation
// (SIGTERM), the for-loop exits at the next iteration boundary.
func (w *Watchdog) Sweep(ctx context.Context) error {
	stuck, err := w.orders.FindStuckFailed(ctx, w.cfg.StuckThreshold, w.cfg.BatchSize)
	if err != nil {
		// Increment dedicated counter so dashboards / alerts can
		// distinguish "orders are stuck" (gauge > 0) from "watchdog
		// itself is broken" (gauge stale, this counter rising).
		observability.SagaWatchdogFindStuckErrorsTotal.Inc()
		return fmt.Errorf("saga watchdog Sweep: find stuck: %w", err)
	}

	// Update gauge so dashboards reflect "as of last sweep" backlog.
	// Set to len(stuck) NOT cumulative — gauge semantics are point-in-time.
	observability.SagaStuckFailedOrders.Set(float64(len(stuck)))

	if len(stuck) == 0 {
		w.log.Debug(ctx, "saga watchdog sweep: no stuck orders")
		return nil
	}
	w.log.Info(ctx, "saga watchdog sweep starting",
		mlog.Int("found", len(stuck)),
		mlog.Duration("oldest_age", stuck[0].Age), // FindStuckFailed returns ORDER BY updated_at ASC
	)

	for _, s := range stuck {
		if err := ctx.Err(); err != nil {
			w.log.Info(ctx, "saga watchdog sweep: ctx cancelled, exiting at loop boundary", tag.Error(err))
			return err
		}
		w.resolve(ctx, s)
	}

	w.log.Info(ctx, "saga watchdog sweep complete", mlog.Int("processed", len(stuck)))
	return nil
}

// resolve handles ONE stuck-Failed order — the per-iteration body of
// Sweep. All per-order failures are logged + metricked but never
// returned; the goal is to resolve as many orders per sweep as
// possible.
//
// Branches (each emits a distinct `outcome` label so operators can
// triage with the right runbook — pre-N4 a single label `compensator_error`
// conflated three failure modes, sending on-call to the wrong page):
//
//  1. Order age > MaxFailedAge → outcome="max_age_exceeded".
//     Do NOT auto-transition: moving Failed → Compensated without
//     verifying inventory was reverted is unsafe. Operator alert
//     fires on `saga_watchdog_resolved_total{outcome="max_age_exceeded"}`
//     for manual review via `order_status_history`.
//  2. orderRepo.GetByID returns infrastructure error → outcome="getbyid_error".
//     Distinct from compensator-side errors — operator should check
//     DB connectivity, not Redis or the compensator code path.
//  3. orderRepo.GetByID returns Compensated (race won by saga consumer)
//     → outcome="already_compensated". Benign skip; signals operator
//     that the regular path is healthy.
//  4. json.Marshal of OrderFailedEvent fails → outcome="marshal_error".
//     Theoretical for the fixed-shape struct today; isolated label
//     means a future field-shape regression is observable.
//  5. compensator.HandleOrderFailed returns nil → outcome="compensated".
//     Trust the compensator's documented idempotency contract
//     (UoW-internal `OrderStatusCompensated` check) — no post-call
//     verification needed.
//  6. compensator.HandleOrderFailed returns error → outcome="compensator_error".
//     Operator should check Redis revert path, DB lock contention,
//     and the compensator code. Will retry next sweep.
func (w *Watchdog) resolve(parent context.Context, s domain.StuckFailed) {
	start := time.Now()
	defer func() {
		observability.SagaWatchdogResolveDurationSeconds.Observe(time.Since(start).Seconds())
		observability.SagaWatchdogResolveAgeSeconds.Observe(s.Age.Seconds())
	}()

	// 1. Max-age give-up. Unlike the reconciler's force-fail path,
	// the watchdog cannot safely auto-transition: Failed → Compensated
	// requires Redis inventory to be actually reverted (otherwise we
	// have a phantom-revert state-machine corruption). The right
	// answer is to alert and let an operator decide.
	if s.Age > w.cfg.MaxFailedAge {
		w.log.Error(parent, "saga watchdog: order exceeds max failed age — manual review required",
			tag.OrderID(s.ID),
			mlog.Duration("age", s.Age),
			mlog.Duration("max_age", w.cfg.MaxFailedAge),
		)
		observability.SagaWatchdogResolvedTotal.WithLabelValues("max_age_exceeded").Inc()
		return
	}

	// 2. Fetch the order to build the OrderFailedEvent payload. The
	// compensator expects an event payload (matches the Kafka contract);
	// we synthesize one from the persisted Order.
	//
	// On error: distinct `getbyid_error` label so operators can see DB-
	// read failures separately from compensator-side failures (different
	// runbook). The "will retry" caveat is conditional: if the DB is
	// healthy on the next sweep, this order is retried; if the DB is
	// down, FindStuckFailed itself fails first and the sweep aborts at
	// that level.
	order, err := w.orders.GetByID(parent, s.ID)
	if err != nil {
		w.log.Warn(parent, "saga watchdog: GetByID failed; if DB recovers next sweep retries, sustained DB outage will fail FindStuckFailed first",
			tag.OrderID(s.ID), tag.Error(err))
		observability.SagaWatchdogResolvedTotal.WithLabelValues("getbyid_error").Inc()
		return
	}

	// Idempotency guard: if the saga consumer's regular path won the
	// race between FindStuckFailed and now, the order is already at
	// Compensated. Count + skip — no need to invoke the compensator
	// (its internal idempotency would also handle this, but counting
	// the race separately gives operators a benign-vs-real signal).
	if order.Status() == domain.OrderStatusCompensated {
		observability.SagaWatchdogResolvedTotal.WithLabelValues("already_compensated").Inc()
		w.log.Info(parent, "saga watchdog: order already compensated (race won by saga consumer)",
			tag.OrderID(s.ID))
		return
	}

	// 3. Synthesize the wire payload + re-drive the compensator. The
	// compensator's Redis revert is idempotent via SETNX on
	// `saga:reverted:<order_id>`; the DB MarkCompensated is idempotent
	// via the OrderStatusCompensated check inside its UoW closure.
	event := application.NewOrderFailedEvent(application.OrderCreatedEvent{
		OrderID:  order.ID(),
		EventID:  order.EventID(),
		UserID:   order.UserID(),
		Quantity: order.Quantity(),
	}, "saga_watchdog: re-driven from stuck Failed state")

	payload, err := json.Marshal(event)
	if err != nil {
		// Theoretical for the fixed-shape OrderFailedEvent struct, but
		// the explicit error path keeps us out of silent-empty-payload
		// territory if a future field addition introduces non-marshalable
		// types. Distinct `marshal_error` label so a regression here
		// is visible without conflating with real compensator failures.
		w.log.Error(parent, "saga watchdog: marshal OrderFailedEvent failed",
			tag.OrderID(s.ID), tag.Error(err))
		observability.SagaWatchdogResolvedTotal.WithLabelValues("marshal_error").Inc()
		return
	}

	if err := w.compensator.HandleOrderFailed(parent, payload); err != nil {
		// Compensator failed (Redis blip, DB lock, etc). Will retry
		// next sweep — the underlying revert + MarkCompensated are
		// idempotent so a partially-completed earlier attempt is
		// safe to redo.
		w.log.Warn(parent, "saga watchdog: compensator failed, will retry next sweep",
			tag.OrderID(s.ID), tag.Error(err))
		observability.SagaWatchdogResolvedTotal.WithLabelValues("compensator_error").Inc()
		return
	}

	observability.SagaWatchdogResolvedTotal.WithLabelValues("compensated").Inc()
	w.log.Info(parent, "saga watchdog: resolved failed→compensated",
		tag.OrderID(s.ID), mlog.Duration("age", s.Age))
}

// (Loop orchestration intentionally lives in cmd/booking-cli/saga_watchdog.go,
// matching the Reconciler pattern — see internal/application/recon/reconciler.go
// which also exposes only Sweep + resolve, with the ticker loop in cmd/.
// Single-source-of-truth for sweep semantics is `Sweep`; orchestration
// is the cmd's concern.)

