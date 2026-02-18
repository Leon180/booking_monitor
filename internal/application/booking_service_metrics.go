package application

import (
	"context"
	"errors"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/observability"
)

type bookingServiceMetricsDecorator struct {
	next BookingService
}

func NewBookingServiceMetricsDecorator(next BookingService) BookingService {
	return &bookingServiceMetricsDecorator{next: next}
}

func (d *bookingServiceMetricsDecorator) BookTicket(ctx context.Context, userID, eventID, quantity int) error {
	err := d.next.BookTicket(ctx, userID, eventID, quantity)

	switch {
	case err == nil:
		observability.BookingsTotal.WithLabelValues("success").Inc()
	case errors.Is(err, domain.ErrSoldOut):
		observability.BookingsTotal.WithLabelValues("sold_out").Inc()
	default:
		// ErrUserAlreadyBought is now only returned by the worker (async DB constraint).
		// BookTicket at the API layer will never return it, so we treat unknowns as errors.
		observability.BookingsTotal.WithLabelValues("error").Inc()
	}

	return err
}

func (d *bookingServiceMetricsDecorator) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]*domain.Order, int, error) {
	return d.next.GetBookingHistory(ctx, page, pageSize, status)
}
