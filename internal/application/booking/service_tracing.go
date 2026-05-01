package booking

import (
	"context"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"booking_monitor/internal/domain"
)

type tracingDecorator struct {
	next Service
}

// NewTracingDecorator wraps the booking service with OTEL spans.
func NewTracingDecorator(next Service) Service {
	return &tracingDecorator{next: next}
}

func (s *tracingDecorator) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (domain.Order, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "BookTicket", trace.WithAttributes(
		attribute.Int("user_id", userID),
		attribute.String("event_id", eventID.String()),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	order, err := s.next.BookTicket(ctx, userID, eventID, quantity)
	if err != nil {
		// RecordError adds the exception event but doesn't change the
		// span status. SetStatus(Error) is what flips the span to red
		// in Jaeger / Grafana Tempo / Datadog APM dashboards. Without
		// it, error spans look identical to successful ones in the
		// "% errored" rollups — silent observability gap.
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		// Tag the span with the minted order_id so trace lookups in
		// Jaeger from a customer-reported order_id resolve back to
		// this BookTicket span (and from there, the upstream HTTP
		// span via the trace tree).
		span.SetAttributes(attribute.String("order_id", order.ID().String()))
	}
	return order, err
}

func (s *tracingDecorator) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetBookingHistory", trace.WithAttributes(
		attribute.Int("page", page),
		attribute.Int("page_size", pageSize),
		attribute.String("status", string(lo.FromPtr(status))),
	))
	defer span.End()

	orders, total, err := s.next.GetBookingHistory(ctx, page, pageSize, status)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return orders, total, err
}

func (s *tracingDecorator) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetOrder", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	order, err := s.next.GetOrder(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return order, err
}
