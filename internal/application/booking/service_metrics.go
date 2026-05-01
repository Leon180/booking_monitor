package booking

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

type metricsDecorator struct {
	next    Service
	metrics Metrics
}

// NewMetricsDecorator wraps the booking service with a metrics
// decorator. The caller provides the Metrics implementation (typically
// Prometheus-backed from the observability package) — keeps
// application independent of infrastructure.
func NewMetricsDecorator(next Service, metrics Metrics) Service {
	return &metricsDecorator{next: next, metrics: metrics}
}

func (d *metricsDecorator) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (domain.Order, error) {
	order, err := d.next.BookTicket(ctx, userID, eventID, quantity)

	switch {
	case err == nil:
		d.metrics.RecordBookingOutcome("success")
	case errors.Is(err, domain.ErrSoldOut):
		d.metrics.RecordBookingOutcome("sold_out")
	default:
		// ErrUserAlreadyBought is now only returned by the worker (async DB constraint).
		// BookTicket at the API layer will never return it, so we treat unknowns as errors.
		// CP2.6: this default also covers domain.ErrInvalidUserID /
		// ErrInvalidQuantity / ErrInvalidOrderID / ErrInvalidEventID
		// from the upfront NewOrder call — invariant violations now
		// surface here as `outcome=error` rather than as worker DLQ
		// entries.
		d.metrics.RecordBookingOutcome("error")
	}

	return order, err
}

func (d *metricsDecorator) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	return d.next.GetBookingHistory(ctx, page, pageSize, status)
}

func (d *metricsDecorator) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return d.next.GetOrder(ctx, id)
}
