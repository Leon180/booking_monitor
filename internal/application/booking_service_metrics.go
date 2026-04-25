package application

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

type bookingServiceMetricsDecorator struct {
	next    BookingService
	metrics BookingMetrics
}

// NewBookingServiceMetricsDecorator wraps the booking service with a
// metrics decorator. The caller provides the BookingMetrics
// implementation (typically Prometheus-backed from the observability
// package) — keeps application independent of infrastructure.
func NewBookingServiceMetricsDecorator(next BookingService, metrics BookingMetrics) BookingService {
	return &bookingServiceMetricsDecorator{next: next, metrics: metrics}
}

func (d *bookingServiceMetricsDecorator) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) error {
	err := d.next.BookTicket(ctx, userID, eventID, quantity)

	switch {
	case err == nil:
		d.metrics.RecordBookingOutcome("success")
	case errors.Is(err, domain.ErrSoldOut):
		d.metrics.RecordBookingOutcome("sold_out")
	default:
		// ErrUserAlreadyBought is now only returned by the worker (async DB constraint).
		// BookTicket at the API layer will never return it, so we treat unknowns as errors.
		d.metrics.RecordBookingOutcome("error")
	}

	return err
}

func (d *bookingServiceMetricsDecorator) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	return d.next.GetBookingHistory(ctx, page, pageSize, status)
}
