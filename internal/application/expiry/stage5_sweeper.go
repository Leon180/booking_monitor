package expiry

import (
	"context"
	"errors"
	"time"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// Stage5Sweeper periodically finds awaiting_payment orders past their
// reservation deadline and calls Compensator.Compensate for each —
// revert Redis inventory + update PG status in a single transaction.
//
// This is the Stage 5 expiry path. It differs from the D6 Sweeper
// (which marks orders expired and emits order.failed to the outbox for
// the saga compensator) because Stage 5 has no outbox/saga: compensation
// is synchronous and in-process.
type Stage5Sweeper struct {
	orders      domain.OrderRepository
	compensator Compensator
	cfg         Config
	log         *mlog.Logger
}

// NewStage5Sweeper constructs the sweeper. cfg.Validate() is the
// caller's responsibility — wiring code must surface invalid config as
// a fatal startup error.
func NewStage5Sweeper(
	orders domain.OrderRepository,
	compensator Compensator,
	cfg Config,
	logger *mlog.Logger,
) *Stage5Sweeper {
	return &Stage5Sweeper{
		orders:      orders,
		compensator: compensator,
		cfg:         cfg,
		log:         logger.With(mlog.String("component", "stage5_expiry_sweeper")),
	}
}

// SweepInterval exposes the loop cadence so the cmd-side ticker can read
// it without duplicating the config translation. Mirrors Sweeper.SweepInterval.
func (s *Stage5Sweeper) SweepInterval() time.Duration { return s.cfg.SweepInterval }

// Sweep runs one sweep cycle: find overdue awaiting_payment orders, call
// Compensate for each. Per-row ErrCompensateNotEligible is silently
// skipped (expected from sweeper-vs-confirm races). Other per-row errors
// are logged so operators see them; the sweep continues to the next row.
func (s *Stage5Sweeper) Sweep(ctx context.Context) {
	expired, err := s.orders.FindExpiredReservations(ctx, s.cfg.ExpiryGracePeriod, s.cfg.BatchSize)
	if err != nil {
		s.log.Error(ctx, "stage5 sweeper: find candidates failed", tag.Error(err))
		return
	}

	for _, e := range expired {
		if ctx.Err() != nil {
			return
		}
		if err := s.compensator.Compensate(ctx, e.ID); err != nil {
			if errors.Is(err, ErrCompensateNotEligible) {
				continue
			}
			s.log.Error(ctx, "stage5 sweeper: compensate failed",
				tag.OrderID(e.ID), tag.Error(err))
		}
	}
}
