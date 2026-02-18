package postgres

import (
	"context"

	"booking_monitor/internal/domain"

	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "infrastructure/persistence/postgres"

// --- EventRepositoryDecorator ---

type eventRepositoryTracingDecorator struct {
	next domain.EventRepository
}

func NewEventRepositoryTracingDecorator(next domain.EventRepository) domain.EventRepository {
	return &eventRepositoryTracingDecorator{next: next}
}

func (d *eventRepositoryTracingDecorator) Create(ctx context.Context, event *domain.Event) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "CreateEvent", trace.WithAttributes(
		attribute.Int("event_id", event.ID),
	))
	defer span.End()

	err := d.next.Create(ctx, event)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *eventRepositoryTracingDecorator) GetByID(ctx context.Context, id int) (*domain.Event, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetByID", trace.WithAttributes(attribute.Int("event_id", id)))
	defer span.End()

	event, err := d.next.GetByID(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return event, err
}

func (d *eventRepositoryTracingDecorator) DeductInventory(ctx context.Context, eventID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "DeductInventory", trace.WithAttributes(
		attribute.Int("event_id", eventID),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	err := d.next.DeductInventory(ctx, eventID, quantity)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *eventRepositoryTracingDecorator) DecrementTicket(ctx context.Context, eventID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "DecrementTicket", trace.WithAttributes(
		attribute.Int("event_id", eventID),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	err := d.next.DecrementTicket(ctx, eventID, quantity)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *eventRepositoryTracingDecorator) Update(ctx context.Context, event *domain.Event) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "UpdateEvent", trace.WithAttributes(
		attribute.Int("event_id", event.ID),
	))
	defer span.End()

	err := d.next.Update(ctx, event)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

// --- OrderRepositoryDecorator ---

type orderRepositoryTracingDecorator struct {
	next domain.OrderRepository
}

func NewOrderRepositoryTracingDecorator(next domain.OrderRepository) domain.OrderRepository {
	return &orderRepositoryTracingDecorator{next: next}
}

func (d *orderRepositoryTracingDecorator) Create(ctx context.Context, order *domain.Order) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "CreateOrder", trace.WithAttributes(
		attribute.Int("user_id", order.UserID),
		attribute.Int("event_id", order.EventID),
	))
	defer span.End()

	err := d.next.Create(ctx, order)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *orderRepositoryTracingDecorator) ListOrders(ctx context.Context, limit, offset int, status *domain.OrderStatus) ([]*domain.Order, int, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListOrders", trace.WithAttributes(
		attribute.Int("limit", limit),
		attribute.Int("offset", offset),
		attribute.String("status", string(lo.FromPtr(status))),
	))
	defer span.End()

	orders, total, err := d.next.ListOrders(ctx, limit, offset, status)
	if err != nil {
		span.RecordError(err)
	}
	return orders, total, err
}
