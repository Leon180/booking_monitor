package application

import (
	"booking_monitor/internal/domain"
	"context"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type bookingServiceTracingDecorator struct {
	next BookingService
}

func NewBookingServiceTracingDecorator(next BookingService) BookingService {
	return &bookingServiceTracingDecorator{next: next}
}

func (s *bookingServiceTracingDecorator) BookTicket(ctx context.Context, userID, eventID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "BookTicket", trace.WithAttributes(
		attribute.Int("user_id", userID),
		attribute.Int("event_id", eventID),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	err := s.next.BookTicket(ctx, userID, eventID, quantity)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (s *bookingServiceTracingDecorator) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]*domain.Order, int, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetBookingHistory", trace.WithAttributes(
		attribute.Int("page", page),
		attribute.Int("page_size", pageSize),
		attribute.String("status", string(lo.FromPtr(status))),
	))
	defer span.End()

	orders, total, err := s.next.GetBookingHistory(ctx, page, pageSize, status)
	if err != nil {
		span.RecordError(err)
	}
	return orders, total, err
}
