// Package recon is the reconciler — a sweeper that resolves orders
// stuck in OrderStatusCharging by querying the payment gateway and
// transitioning them to Confirmed/Failed. It runs as its own
// `booking-cli recon` subcommand, separate from the payment worker
// process, so a buggy reconciler can't deadlock the order-processing
// hot path.
//
// Design summary (full spec in docs/PROJECT_SPEC.md §A4):
//
//  1. Every sweep cycle:
//     - SELECT id, age FROM orders WHERE status='charging' AND
//     updated_at < NOW() - threshold ORDER BY updated_at ASC LIMIT batch
//     (uses the partial index from migration 000010)
//     - For each result, in its own per-order context (timeout):
//     - If age > MaxChargingAge → MarkFailed (give up); emit metric
//     - Else gateway.GetStatus(id) → switch on outcome:
//     - Charged   → MarkConfirmed
//     - Declined  → MarkFailed
//     - NotFound  → MarkFailed (no gateway record after threshold; stuck due to crash before Charge)
//     - Unknown   → skip; emit metric (will retry next sweep)
//     - error     → skip; emit error metric (transient infra failure)
//  2. Exit cleanly on ctx.Done at loop boundaries; finish current order
//     under its own ctx so SIGTERM-triggered shutdown doesn't leave
//     half-resolved state.
//  3. Two run modes (cmd/booking-cli/recon.go):
//     - Default loop: ticker-driven, runs until SIGTERM
//     - --once: single sweep then exit (k8s CronJob mode)
package recon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// Reconciler holds the dependencies + tunables for the sweep. Built
// once at process start and reused across every sweep cycle.
//
// Notice this struct embeds `domain.PaymentStatusReader`, NOT the
// full `domain.PaymentGateway` — the reconciler MUST NOT have
// `Charge` in scope (defense-in-depth against accidental
// double-charge bugs in recon code). Mirrors `io.Reader` / `io.Writer`
// separation in the standard library.
type Reconciler struct {
	orders  domain.OrderRepository
	gateway domain.PaymentStatusReader
	cfg     config.ReconConfig
	log     *mlog.Logger
}

// NewReconciler is the fx-friendly constructor. Decorates the logger
// with `component=recon` so every log line is filterable in
// Loki/Grafana without per-call ceremony.
func NewReconciler(
	orders domain.OrderRepository,
	gateway domain.PaymentStatusReader,
	cfg *config.Config,
	logger *mlog.Logger,
) *Reconciler {
	return &Reconciler{
		orders:  orders,
		gateway: gateway,
		cfg:     cfg.Recon,
		log:     logger.With(mlog.String("component", "recon")),
	}
}

// Sweep runs ONE reconciliation cycle: scan for stuck-Charging orders,
// resolve each, return. Both run modes (loop and --once) call this
// — the loop just calls it on a ticker, the --once mode calls it once
// and exits. Single source of truth for sweep semantics.
//
// Returns the first non-recoverable error encountered. Per-order
// failures (gateway transient error, ErrInvalidTransition race) are
// logged + counted but do NOT abort the sweep — we still want the
// remaining stuck orders resolved this cycle.
//
// ctx semantics: the caller's ctx caps the WHOLE sweep. Each per-order
// resolve derives its own time-bounded child ctx for the gateway call
// (cfg.GatewayTimeout). On parent ctx cancellation (SIGTERM), the
// outer for-loop exits at the next iteration boundary; the
// currently-resolving order completes under its own derived ctx.
func (r *Reconciler) Sweep(ctx context.Context) error {
	stuck, err := r.orders.FindStuckCharging(ctx, r.cfg.ChargingThreshold, r.cfg.BatchSize)
	if err != nil {
		// Increment the dedicated counter so dashboards / alerts can
		// distinguish "orders are stuck" (gauge > 0) from "recon
		// itself is broken" (gauge stale, this counter rising).
		// Without this, a missing-migration / DB-outage scenario
		// shows nothing on dashboards except a stale gauge.
		observability.ReconFindStuckErrorsTotal.Inc()
		return fmt.Errorf("recon Sweep: find stuck: %w", err)
	}

	// Update gauge so dashboards reflect "as of last sweep" backlog.
	// Set to len(stuck) NOT cumulative; gauge semantics are point-in-time.
	observability.ReconStuckChargingOrders.Set(float64(len(stuck)))

	if len(stuck) == 0 {
		r.log.Debug(ctx, "recon sweep: no stuck orders")
		return nil
	}
	r.log.Info(ctx, "recon sweep starting",
		mlog.Int("found", len(stuck)),
		mlog.Duration("oldest_age", stuck[0].Age), // FindStuckCharging returns ORDER BY updated_at ASC
	)

	for _, s := range stuck {
		// Exit cleanly at the loop boundary if the caller's ctx is
		// done. The current order is NOT cancelled mid-resolve —
		// each resolve runs under its own derived ctx (resolve(ctx,
		// s) below) so SIGTERM lets the in-flight gateway call
		// finish gracefully before we exit. We don't log a "remaining"
		// count because tracking the per-iteration index just to
		// produce one log field is more code than the field is
		// worth — operators can read recon_resolved_total deltas
		// from before / after the cancel for the same information.
		if err := ctx.Err(); err != nil {
			r.log.Info(ctx, "recon sweep: ctx cancelled, exiting at loop boundary", tag.Error(err))
			return err
		}
		r.resolve(ctx, s)
	}

	r.log.Info(ctx, "recon sweep complete", mlog.Int("processed", len(stuck)))
	return nil
}

// resolve handles ONE stuck order — the per-iteration body of Sweep.
// All per-order failures are logged + metricked but never returned;
// the goal is to resolve as many orders per sweep as possible.
//
// Three branches:
//
//  1. Order age > MaxChargingAge → force-fail (give up).
//     Triggered when a gateway has been persistently Unknown or
//     unreachable for too long. Operator alert fires; manual review.
//  2. gateway.GetStatus returns infrastructure error → skip + count.
//     Will retry next sweep.
//  3. gateway.GetStatus returns ChargeStatus verdict → resolve.
//     Charged   → MarkConfirmed
//     Declined  → MarkFailed
//     NotFound  → MarkFailed
//     Unknown   → skip + count (will retry next sweep)
func (r *Reconciler) resolve(parent context.Context, s domain.StuckCharging) {
	start := time.Now()
	defer func() {
		observability.ReconResolveDurationSeconds.Observe(time.Since(start).Seconds())
		observability.ReconResolveAgeSeconds.Observe(s.Age.Seconds())
	}()

	// 1. Max-age give-up. Force-fail the order so the persistent
	// "stuck Charging" alert clears and a different alert
	// (recon_resolved_total{outcome="max_age_exceeded"}) lights up
	// for the operator. This is the escape hatch for permanently-
	// Unknown orders; without it, an alert fires forever with no
	// remediation path.
	if s.Age > r.cfg.MaxChargingAge {
		r.log.Warn(parent, "recon: order exceeds max charging age, force-failing",
			tag.OrderID(s.ID),
			mlog.Duration("age", s.Age),
			mlog.Duration("max_age", r.cfg.MaxChargingAge),
		)
		if err := r.orders.MarkFailed(parent, s.ID); err != nil {
			r.handleMarkErr(parent, s.ID, "max_age_exceeded", err)
			return
		}
		observability.ReconResolvedTotal.WithLabelValues("max_age_exceeded").Inc()
		return
	}

	// 2. Per-order gateway timeout. Without this, a hung gateway pins
	// the whole sweep loop — and SIGTERM can't preempt a blocked
	// HTTP read.
	//
	// Note the asymmetry: gwCtx is used ONLY for gateway.GetStatus
	// below. Mark{Confirmed,Failed} calls use the parent ctx — the
	// DB write must NOT share the gateway's budget (a gateway that
	// burns 4.9s of a 5s budget would leave 100ms for MarkConfirmed,
	// which is unsafe). Parent ctx still propagates SIGTERM cleanly
	// to Mark*; only the timeout differs.
	gwCtx, cancel := context.WithTimeout(parent, r.cfg.GatewayTimeout)
	defer cancel()

	gwStart := time.Now()
	status, err := r.gateway.GetStatus(gwCtx, s.ID)
	observability.ReconGatewayDurationSeconds.Observe(time.Since(gwStart).Seconds())

	if err != nil {
		// Distinguish the (Unknown, err) infrastructure-failure case
		// from the (Unknown, nil) verdict case. err != nil = transient
		// infra problem (network, gateway 5xx, ctx timeout). Skip and
		// retry next sweep — emit a dedicated counter so a sustained
		// rate is alertable.
		r.log.Warn(parent, "recon: gateway GetStatus failed, will retry next sweep",
			tag.OrderID(s.ID), tag.Error(err))
		observability.ReconGatewayErrorsTotal.Inc()
		return
	}

	// 3. Apply the verdict.
	switch status {
	case domain.ChargeStatusCharged:
		if err := r.orders.MarkConfirmed(parent, s.ID); err != nil {
			r.handleMarkErr(parent, s.ID, "charged", err)
			return
		}
		observability.ReconResolvedTotal.WithLabelValues("charged").Inc()
		r.log.Info(parent, "recon: resolved charging→confirmed",
			tag.OrderID(s.ID), mlog.Duration("age", s.Age))

	case domain.ChargeStatusDeclined:
		if err := r.orders.MarkFailed(parent, s.ID); err != nil {
			r.handleMarkErr(parent, s.ID, "declined", err)
			return
		}
		observability.ReconResolvedTotal.WithLabelValues("declined").Inc()
		r.log.Info(parent, "recon: resolved charging→failed (declined)",
			tag.OrderID(s.ID), mlog.Duration("age", s.Age))

	case domain.ChargeStatusNotFound:
		// Gateway has no record = the worker crashed AFTER MarkCharging
		// committed but BEFORE Charge was called. Safe to fail the
		// order (the customer was never charged).
		if err := r.orders.MarkFailed(parent, s.ID); err != nil {
			r.handleMarkErr(parent, s.ID, "not_found", err)
			return
		}
		observability.ReconResolvedTotal.WithLabelValues("not_found").Inc()
		r.log.Info(parent, "recon: resolved charging→failed (gateway has no record)",
			tag.OrderID(s.ID), mlog.Duration("age", s.Age))

	default:
		// ChargeStatusUnknown OR any unrecognized value (which the
		// zero-value-is-Unknown design means returns the same way).
		// Skip + count + retry next sweep. Persistent Unknown is
		// caught by the max-age branch above on a future cycle.
		observability.ReconResolvedTotal.WithLabelValues("unknown").Inc()
		r.log.Info(parent, "recon: gateway returned Unknown, skipping",
			tag.OrderID(s.ID), tag.Status(string(status)),
			mlog.Duration("age", s.Age))
	}
}

// handleMarkErr is the shared error-classification path for
// MarkConfirmed / MarkFailed failures. ErrInvalidTransition means the
// row moved between FindStuckCharging and Mark* — typically because
// the original payment worker won the race after our gateway query
// already committed Charge. Idempotent success: count it as a
// "transition_lost" outcome so a sustained rate is visible without
// being treated as an error.
func (r *Reconciler) handleMarkErr(ctx context.Context, id uuid.UUID, outcome string, err error) {
	if errors.Is(err, domain.ErrInvalidTransition) {
		observability.ReconResolvedTotal.WithLabelValues("transition_lost").Inc()
		r.log.Info(ctx, "recon: lost transition race (worker committed first, benign)",
			tag.OrderID(id),
			mlog.String("intended_outcome", outcome), tag.Error(err))
		return
	}
	// Real DB failure path — emit a counter so dashboards / alerts can
	// see sustained DB issues during the resolve path. Without this
	// counter, a sustained DB outage during recon would only surface
	// in logs, and no alert can fire on log lines.
	observability.ReconMarkErrorsTotal.Inc()
	r.log.Error(ctx, "recon: mark transition failed",
		tag.OrderID(id),
		mlog.String("intended_outcome", outcome), tag.Error(err))
}
