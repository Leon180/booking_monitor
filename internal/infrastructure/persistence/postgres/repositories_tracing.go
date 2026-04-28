package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"booking_monitor/internal/domain"
)

const tracerName = "infrastructure/persistence/postgres"

// --- EventRepositoryDecorator ---

type eventRepositoryTracingDecorator struct {
	next domain.EventRepository
}

func NewEventRepositoryTracingDecorator(next domain.EventRepository) domain.EventRepository {
	return &eventRepositoryTracingDecorator{next: next}
}

func (d *eventRepositoryTracingDecorator) Create(ctx context.Context, event domain.Event) (domain.Event, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "CreateEvent", trace.WithAttributes(
		attribute.String("event_id", event.ID().String()),
	))
	defer span.End()

	created, err := d.next.Create(ctx, event)
	if err != nil {
		span.RecordError(err)
	}
	return created, err
}

func (d *eventRepositoryTracingDecorator) GetByID(ctx context.Context, id uuid.UUID) (domain.Event, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetByID", trace.WithAttributes(attribute.String("event_id", id.String())))
	defer span.End()

	event, err := d.next.GetByID(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return event, err
}

func (d *eventRepositoryTracingDecorator) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (domain.Event, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetByIDForUpdate", trace.WithAttributes(
		attribute.String("event_id", id.String()),
	))
	defer span.End()

	event, err := d.next.GetByIDForUpdate(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return event, err
}

func (d *eventRepositoryTracingDecorator) DecrementTicket(ctx context.Context, eventID uuid.UUID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "DecrementTicket", trace.WithAttributes(
		attribute.String("event_id", eventID.String()),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	err := d.next.DecrementTicket(ctx, eventID, quantity)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *eventRepositoryTracingDecorator) IncrementTicket(ctx context.Context, eventID uuid.UUID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "IncrementTicket", trace.WithAttributes(
		attribute.String("event_id", eventID.String()),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	err := d.next.IncrementTicket(ctx, eventID, quantity)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *eventRepositoryTracingDecorator) Update(ctx context.Context, event domain.Event) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "UpdateEvent", trace.WithAttributes(
		attribute.String("event_id", event.ID().String()),
	))
	defer span.End()

	err := d.next.Update(ctx, event)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *eventRepositoryTracingDecorator) Delete(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "DeleteEvent", trace.WithAttributes(
		attribute.String("event_id", id.String()),
	))
	defer span.End()

	err := d.next.Delete(ctx, id)
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

func (d *orderRepositoryTracingDecorator) Create(ctx context.Context, order domain.Order) (domain.Order, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "CreateOrder", trace.WithAttributes(
		attribute.Int("user_id", order.UserID()),
		attribute.String("event_id", order.EventID().String()),
	))
	defer span.End()

	created, err := d.next.Create(ctx, order)
	if err != nil {
		span.RecordError(err)
	}
	return created, err
}

func (d *orderRepositoryTracingDecorator) GetByID(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetOrderByID", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	order, err := d.next.GetByID(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return order, err
}

func (d *orderRepositoryTracingDecorator) ListOrders(ctx context.Context, limit, offset int, status *domain.OrderStatus) ([]domain.Order, int, error) {
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

func (d *orderRepositoryTracingDecorator) FindStuckCharging(ctx context.Context, minAge time.Duration, limit int) ([]domain.StuckCharging, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "FindStuckCharging", trace.WithAttributes(
		attribute.String("min_age", minAge.String()),
		attribute.Int("limit", limit),
	))
	defer span.End()

	stuck, err := d.next.FindStuckCharging(ctx, minAge, limit)
	if err != nil {
		span.RecordError(err)
	}
	span.SetAttributes(attribute.Int("found", len(stuck)))
	return stuck, err
}

func (d *orderRepositoryTracingDecorator) MarkCharging(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "MarkOrderCharging", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.MarkCharging(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *orderRepositoryTracingDecorator) MarkConfirmed(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "MarkOrderConfirmed", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.MarkConfirmed(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *orderRepositoryTracingDecorator) MarkFailed(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "MarkOrderFailed", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.MarkFailed(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *orderRepositoryTracingDecorator) MarkCompensated(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "MarkOrderCompensated", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.MarkCompensated(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}
