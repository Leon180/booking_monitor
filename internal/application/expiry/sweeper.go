// Package expiry is the D6 reservation expiry sweeper — periodically
// scans `orders WHERE status='awaiting_payment' AND reserved_until <=
// NOW() - GracePeriod`, atomically transitions each row to `expired`,
// and emits `order.failed` to the outbox in the same UoW. The
// existing saga compensator (D2 widened `MarkCompensated` to accept
// `Expired → Compensated`) consumes `order.failed` and reverts the
// Redis-side inventory deduct that's been soft-locking the ticket
// since `BookingService.BookTicket`.
//
// Runs as its own `booking-cli expiry-sweeper` subcommand, separate
// from the saga consumer / saga watchdog / payment worker, so a buggy
// sweeper can't deadlock other processes.
//
// Design summary (full spec in docs/PROJECT_SPEC.md §D6):
//
//  1. Every sweep cycle:
//     - SELECT id, age FROM orders WHERE status='awaiting_payment' AND
//       reserved_until <= NOW() - $1::interval ORDER BY reserved_until
//       ASC LIMIT batch (uses partial index from migration 000012)
//     - For each result: GetByID outside UoW (need Order to build
//       failed-event payload), then UoW: MarkExpired → marshal →
//       Outbox.Create.
//     - At sweep END: CountOverdueAfterCutoff to update backlog gauges.
//  2. **Always expires eligible rows** (round-1 P1 contract). Rows
//     past MaxAge get the `expired_overaged` outcome label + bump the
//     dedicated max-age counter, but they ARE still transitioned —
//     skipping them re-creates the soft-lock problem D6 exists to
//     solve. Saga compensator's `saga:reverted:order:<id>` SETNX
//     guard makes the re-emit idempotent.
//  3. **DB-NOW as single time source** (round-2 F1). The cutoff is
//     computed in SQL via `NOW() - $1::interval`, sharing the same
//     time source as D5's `MarkPaid` predicate (`reserved_until >
//     NOW()`). Avoids app-clock skew producing premature expiry.
//  4. Two run modes (cmd/booking-cli/expiry_sweeper.go):
//     - Default loop: ticker-driven, runs until SIGTERM
//     - --once: single sweep then exit (k8s CronJob mode). On error,
//       exit code 1 via `shutdowner.Shutdown(fx.ExitCode(1))`.
//
// Why D6 doesn't call revert.lua directly:
//   Layering — D6 owns timing (when does the row expire), saga owns
//   inventory revert (how does Redis become consistent). Same shape
//   as D5's failure path. Reasons: (a) reuses compensator's
//   idempotent SETNX-keyed revert guard; (b) compensator handles
//   legacy v<3 fallback + multi-ticket-type corruption path — D6
//   calling Redis directly would duplicate all that; (c) decoupling
//   lets compensator catch up after a D6 burst without back-pressuring
//   the sweeper; (d) failure isolation — Redis blip during D6 doesn't
//   fail the SQL transition.
package expiry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// errOutboxCreateFailed wraps `Outbox.Create` failures inside the
// UoW closure so the post-UoW classifier can distinguish them from
// `MarkExpired` failures (both would otherwise collapse to
// `transition_error`, sending operators to the wrong runbook —
// orders-table troubleshooting vs events_outbox-table). Package-
// private since only the sweeper care about the distinction.
var errOutboxCreateFailed = errors.New("expiry sweeper: outbox create failed")

// Sweeper holds the dependencies + tunables. Built once at process
// start and reused across every sweep cycle. Depends only on domain
// ports + application-layer types — no `infrastructure/config` or
// `infrastructure/observability` imports.
type Sweeper struct {
	orders  domain.OrderRepository
	uow     application.UnitOfWork
	cfg     Config
	metrics Metrics
	log     *mlog.Logger
	now     func() time.Time // injectable for tests
}

// NewSweeper is the constructor. The bootstrap layer (or each cmd
// entry) is responsible for translating
// `infrastructure/config.ExpiryConfig` into `expiry.Config` and for
// providing a Metrics implementation (Prometheus-backed in
// production, NopMetrics or a fake in tests).
//
// `cfg.Validate()` is the caller's responsibility — startup must
// surface invalid configuration as a fatal, not silently run with
// broken tunables.
func NewSweeper(
	orders domain.OrderRepository,
	uow application.UnitOfWork,
	cfg Config,
	metrics Metrics,
	logger *mlog.Logger,
) *Sweeper {
	return &Sweeper{
		orders:  orders,
		uow:     uow,
		cfg:     cfg,
		metrics: metrics,
		log:     logger.With(mlog.String("component", "expiry_sweeper")),
		now:     time.Now,
	}
}

// SweepInterval exposes the loop cadence so the cmd-side ticker can
// read it without duplicating the config-translation step. Mirrors
// `saga.Watchdog.WatchdogInterval()`. Read-only.
func (s *Sweeper) SweepInterval() time.Duration { return s.cfg.SweepInterval }

// Sweep runs ONE sweeper cycle: scan for overdue awaiting_payment
// orders, resolve each, then update backlog observability. Both run
// modes (loop and --once) call this. Single source of truth for sweep
// semantics.
//
// Returns the first non-recoverable error:
//   - `FindExpiredReservations` failure → `expiry_find_expired_errors_total++`
//     + return. (Loop mode logs and waits for next tick; --once mode
//     exits with code 1.)
//   - `CountOverdueAfterCutoff` failure → same counter + WARN log +
//     gauges held at last-known-good + return error. Already-resolved
//     rows in this tick are NOT rolled back — count is post-UoW
//     observability, not part of the work itself (round-3 F2 contract).
//
// Per-row failures (GetByID race, MarkExpired transition, Outbox
// write) are logged + counted via `IncResolved` but do NOT abort the
// sweep — we still want the remaining rows resolved this cycle.
//
// ctx semantics: caller's ctx caps the WHOLE sweep. Each per-order
// resolve runs under its own derived ctx; on parent cancellation
// (SIGTERM), the for-loop exits at the next iteration boundary.
func (s *Sweeper) Sweep(ctx context.Context) error {
	sweepStart := s.now()
	defer func() {
		s.metrics.ObserveSweepDuration(s.now().Sub(sweepStart).Seconds())
	}()

	expired, err := s.orders.FindExpiredReservations(ctx, s.cfg.ExpiryGracePeriod, s.cfg.BatchSize)
	if err != nil {
		s.metrics.IncFindExpiredErrors()
		return fmt.Errorf("expiry sweeper Sweep: find expired: %w", err)
	}

	// Sweep-start log only — do NOT write to the gauge here. The
	// gauge update is exclusively at sweep END after a successful
	// CountOverdueAfterCutoff (round-3 F2 contract: on count failure
	// gauges are HELD at last-known-good, NOT overwritten with a
	// mid-sweep input snapshot). An earlier draft set the gauge here
	// from `expired[0].Age` and overwrote it post-sweep — but if the
	// count query failed, the gauge would be left holding the
	// sweep-START input view (stale-this-cycle), not the actual
	// last-known-good from the previous successful sweep, breaking
	// the operator-facing contract documented on the alert.
	if len(expired) > 0 {
		s.log.Info(ctx, "expiry sweep starting",
			mlog.Int("found", len(expired)),
			mlog.Duration("oldest_age", expired[0].Age),
		)
	} else {
		s.log.Debug(ctx, "expiry sweep: no overdue reservations")
	}

	for _, e := range expired {
		if err := ctx.Err(); err != nil {
			s.log.Info(ctx, "expiry sweep: ctx cancelled, exiting at loop boundary", tag.Error(err))
			return err
		}
		s.resolve(ctx, e)
	}

	// Post-sweep observability: count rows STILL overdue after
	// processing this batch. 0 in steady state; non-zero means
	// BatchSize too small for current eligibility rate OR per-row
	// processing erroring out. The query failure path (round-3 F2):
	// counter increments, gauges held at last-known-good, error
	// returned from Sweep so loop logs+continues / --once exits 1.
	count, oldest, countErr := s.orders.CountOverdueAfterCutoff(ctx, s.cfg.ExpiryGracePeriod)
	if countErr != nil {
		s.metrics.IncFindExpiredErrors()
		s.log.Warn(ctx, "expiry sweep: post-sweep count query failed; backlog gauges held at last-known-good",
			tag.Error(countErr))
		return fmt.Errorf("expiry sweeper Sweep: count overdue: %w", countErr)
	}
	s.metrics.SetBacklogAfterSweep(count)
	// Update oldest gauge with post-sweep view: if we drained
	// everything, this is 0; if backlog persists, this reflects the
	// oldest row we couldn't reach this tick.
	s.metrics.SetOldestOverdueAge(oldest.Seconds())

	if len(expired) > 0 {
		s.log.Info(ctx, "expiry sweep complete",
			mlog.Int("processed", len(expired)),
			mlog.Int("backlog_after", count),
		)
	}
	return nil
}

// resolve handles ONE overdue reservation — the per-iteration body of
// Sweep. All per-row failures are logged + metricked but never
// returned; the goal is to resolve as many rows per sweep as possible.
//
// Branches (each emits a distinct `outcome` label so operators can
// triage with the right runbook):
//
//  1. orderRepo.GetByID returns infrastructure error → outcome="getbyid_error".
//     Distinct from transition / outbox errors — operator should check
//     DB connectivity for the GetByID query specifically.
//  2. Order status is no longer awaiting_payment (D5 webhook race won
//     between FindExpiredReservations and now) → outcome="already_terminal".
//     Benign skip; no order.failed emit (D5 already handled it).
//  3. UoW {MarkExpired + emit order.failed}:
//     a. MarkExpired returns ErrInvalidTransition (D5 raced inside
//        the UoW — extremely narrow race, rows are sorted oldest-first
//        but cutoff query sees a stale snapshot vs UoW's row-lock
//        view) → outcome="already_terminal". Log Info.
//     b. MarkExpired returns other error → outcome="transition_error".
//        Log Warn, will retry next sweep.
//     c. json.Marshal of OrderFailedEvent fails → outcome="marshal_error".
//        Theoretical for fixed-shape struct today.
//     d. Outbox.Create fails → outcome="outbox_error". Log Warn.
//     e. UoW commits → outcome="expired" OR "expired_overaged"
//        (depending on age vs MaxAge).
func (s *Sweeper) resolve(parent context.Context, e domain.ExpiredReservation) {
	start := s.now()
	defer func() {
		s.metrics.ObserveResolveDuration(s.now().Sub(start).Seconds())
		s.metrics.ObserveResolveAge(e.Age.Seconds())
	}()

	// 1. Fetch the order to build the OrderFailedEvent payload outside
	// the UoW. The compensator expects an event payload (matches the
	// Kafka contract); we synthesize one from the persisted Order.
	order, err := s.orders.GetByID(parent, e.ID)
	if err != nil {
		s.log.Warn(parent, "expiry sweeper: GetByID failed",
			tag.OrderID(e.ID), tag.Error(err))
		s.metrics.IncResolved(OutcomeGetByIDError)
		return
	}

	// 2. Pre-UoW status guard: if the order is already terminal (D5
	// webhook won the race between FindExpiredReservations and now),
	// skip cleanly. Saga compensator either ran or will run via D5's
	// `order.failed` emit — D6 emitting a second event would be
	// idempotent at the saga's SETNX guard, but counting the race
	// separately gives operators a benign-vs-real signal.
	if order.Status() != domain.OrderStatusAwaitingPayment {
		s.log.Info(parent, "expiry sweeper: order no longer awaiting_payment (D5 race)",
			tag.OrderID(e.ID),
			tag.Status(string(order.Status())))
		s.metrics.IncResolved(OutcomeAlreadyTerminal)
		return
	}

	// 3. MaxAge labeling (round-1 P1 contract: NOT skipping). Compute
	// the outcome label ahead of the UoW so a failure inside still
	// records the right `expired_overaged` vs `expired` dimension on
	// the dedicated max-age counter (alert is on that counter, not
	// on the resolved-vector outcome).
	overAged := e.Age > s.cfg.MaxAge
	if overAged {
		s.log.Warn(parent, "expiry sweeper: row exceeds MaxAge but will still expire (operator awareness only)",
			tag.OrderID(e.ID),
			mlog.Duration("age", e.Age),
			mlog.Duration("max_age", s.cfg.MaxAge),
		)
		// IncMaxAgeExceeded() is INTENTIONALLY NOT called here.
		// The counter feeds `ExpiryMaxAgeExceeded` (critical, single-
		// event paging) whose runbook says "the row is now expired".
		// Incrementing pre-UoW would let the alert fire on rows that
		// haven't actually been expired (UoW failure rolls back, row
		// stays awaiting_payment, counter then double-counts on the
		// next-tick retry). The increment lives next to
		// `OutcomeExpiredOveraged` in the success branch below — the
		// counter then reflects "successfully expired-overaged rows",
		// which is what the alert + runbook actually mean.
	}

	// 4. Atomic transition + outbox emit. Same UoW shape as D5's
	// handleLateSuccess (webhook_service.go:419-445) — keeps the
	// status flip and the saga-trigger event in one transaction so a
	// partial commit can't leave a row marked expired without an
	// outbox event waiting for the compensator.
	failedEvent := application.NewOrderFailedEventFromOrder(
		order,
		"reservation_expired_after: "+order.ReservedUntil().Format(time.RFC3339),
	)
	payload, err := json.Marshal(failedEvent)
	if err != nil {
		s.log.Error(parent, "expiry sweeper: marshal OrderFailedEvent failed",
			tag.OrderID(e.ID), tag.Error(err))
		s.metrics.IncResolved(OutcomeMarshalError)
		return
	}
	outboxEvent, err := domain.NewOrderFailedOutbox(payload)
	if err != nil {
		// `NewOrderFailedOutbox` is pre-marshal payload-construction —
		// classify under `marshal_error`, NOT `outbox_error`. The
		// `outbox_error` label is reserved for actual `Outbox.Create`
		// failures inside the UoW (see sentinel below). This branch
		// fires only on UUID generation failure (rand.Read exhausted)
		// or a future shape invariant — both are payload-build
		// failures, not outbox-write failures.
		s.log.Error(parent, "expiry sweeper: build outbox event failed",
			tag.OrderID(e.ID), tag.Error(err))
		s.metrics.IncResolved(OutcomeMarshalError)
		return
	}

	uowErr := s.uow.Do(parent, func(repos *application.Repositories) error {
		if markErr := repos.Order.MarkExpired(parent, order.ID()); markErr != nil {
			return markErr
		}
		// Wrap Outbox.Create failure with errOutboxCreateFailed so the
		// post-UoW classifier can distinguish it from MarkExpired
		// failure. Without this, both collapse to `transition_error`
		// and the operator gets pointed at the wrong runbook (DB
		// orders-table troubleshooting vs DB events_outbox-table).
		if _, createErr := repos.Outbox.Create(parent, outboxEvent); createErr != nil {
			return fmt.Errorf("%w: %w", errOutboxCreateFailed, createErr)
		}
		return nil
	})

	if uowErr == nil {
		if overAged {
			s.metrics.IncResolved(OutcomeExpiredOveraged)
			// IncMaxAgeExceeded fires HERE (not pre-UoW) so the
			// `ExpiryMaxAgeExceeded` alert reflects "rows we
			// successfully expired past MaxAge", not "rows we attempted
			// to expire but rolled back". Codex round-3 P2 fix.
			s.metrics.IncMaxAgeExceeded()
			s.log.Warn(parent, "expiry sweeper: expired (overaged)",
				tag.OrderID(e.ID), mlog.Duration("age", e.Age))
		} else {
			s.metrics.IncResolved(OutcomeExpired)
			s.log.Info(parent, "expiry sweeper: expired",
				tag.OrderID(e.ID), mlog.Duration("age", e.Age))
		}
		return
	}

	// 5. UoW failures. ErrInvalidTransition = D5 raced inside the UoW
	// (the order flipped between our pre-UoW status read and the
	// MarkExpired SQL). Treat as `already_terminal`.
	if errors.Is(uowErr, domain.ErrInvalidTransition) {
		s.log.Info(parent, "expiry sweeper: D5 raced inside UoW; order no longer awaiting_payment",
			tag.OrderID(e.ID), tag.Error(uowErr))
		s.metrics.IncResolved(OutcomeAlreadyTerminal)
		return
	}

	// Outbox.Create failure is wrapped with errOutboxCreateFailed
	// inside the closure (above) so we can classify it distinctly
	// from MarkExpired failure. Different runbook: outbox failures
	// point at the events_outbox table (schema drift, NULL
	// constraint, disk space), whereas transition errors point at
	// the orders table (DB lock contention, replication lag).
	if errors.Is(uowErr, errOutboxCreateFailed) {
		s.log.Warn(parent, "expiry sweeper: outbox create failed inside UoW, will retry next sweep",
			tag.OrderID(e.ID), tag.Error(uowErr))
		s.metrics.IncResolved(OutcomeOutboxError)
		return
	}

	// Anything else = MarkExpired failure (DB lock, ctx cancellation,
	// replication-blocked write, etc.). Will retry next sweep — both
	// MarkExpired and Outbox.Create are idempotent under the UoW
	// rollback.
	s.log.Warn(parent, "expiry sweeper: MarkExpired failed inside UoW, will retry next sweep",
		tag.OrderID(e.ID), tag.Error(uowErr))
	s.metrics.IncResolved(OutcomeTransitionError)
}
