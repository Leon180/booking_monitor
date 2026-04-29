package application

import (
	"booking_monitor/internal/domain"
	"context"

	"github.com/google/uuid"
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

func (s *bookingServiceTracingDecorator) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (uuid.UUID, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "BookTicket", trace.WithAttributes(
		attribute.Int("user_id", userID),
		attribute.String("event_id", eventID.String()),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	orderID, err := s.next.BookTicket(ctx, userID, eventID, quantity)
	if err != nil {
		span.RecordError(err)
	} else {
		// Tag the span with the minted order_id so trace lookups in
		// Jaeger from a customer-reported order_id resolve back to
		// this BookTicket span (and from there, the upstream HTTP
		// span via the trace tree).
		span.SetAttributes(attribute.String("order_id", orderID.String()))
	}
	return orderID, err
}

func (s *bookingServiceTracingDecorator) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
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

func (s *bookingServiceTracingDecorator) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetOrder", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	order, err := s.next.GetOrder(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return order, err
}
