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

func (d *eventRepositoryTracingDecorator) ListAvailable(ctx context.Context) ([]domain.Event, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListAvailable")
	defer span.End()

	events, err := d.next.ListAvailable(ctx)
	if err != nil {
		span.RecordError(err)
	} else {
		span.SetAttributes(attribute.Int("count", len(events)))
	}
	return events, err
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

func (d *orderRepositoryTracingDecorator) FindStuckFailed(ctx context.Context, minAge time.Duration, limit int) ([]domain.StuckFailed, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "FindStuckFailed", trace.WithAttributes(
		attribute.String("min_age", minAge.String()),
		attribute.Int("limit", limit),
	))
	defer span.End()

	stuck, err := d.next.FindStuckFailed(ctx, minAge, limit)
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

// Pattern A transitions (D2). Same wrapping pattern as the legacy
// methods above — start a span, delegate, RecordError on failure,
// return the wrapped error.

func (d *orderRepositoryTracingDecorator) MarkAwaitingPayment(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "MarkOrderAwaitingPayment", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.MarkAwaitingPayment(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *orderRepositoryTracingDecorator) MarkPaid(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "MarkOrderPaid", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.MarkPaid(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *orderRepositoryTracingDecorator) MarkExpired(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "MarkOrderExpired", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.MarkExpired(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *orderRepositoryTracingDecorator) MarkPaymentFailed(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "MarkOrderPaymentFailed", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.MarkPaymentFailed(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *orderRepositoryTracingDecorator) SetPaymentIntentID(ctx context.Context, id uuid.UUID, paymentIntentID string) error {
	// payment_intent_id is intentionally NOT recorded as a span
	// attribute — Stripe-shape ids embed account context that we
	// shouldn't leak into traces / log aggregators by default.
	ctx, span := otel.Tracer(tracerName).Start(ctx, "SetPaymentIntentID", trace.WithAttributes(
		attribute.String("order_id", id.String()),
	))
	defer span.End()

	err := d.next.SetPaymentIntentID(ctx, id, paymentIntentID)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

// --- TicketTypeRepositoryDecorator (D4.1) ---

type ticketTypeRepositoryTracingDecorator struct {
	next domain.TicketTypeRepository
}

func NewTicketTypeRepositoryTracingDecorator(next domain.TicketTypeRepository) domain.TicketTypeRepository {
	return &ticketTypeRepositoryTracingDecorator{next: next}
}

func (d *ticketTypeRepositoryTracingDecorator) Create(ctx context.Context, t domain.TicketType) (domain.TicketType, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "CreateTicketType", trace.WithAttributes(
		attribute.String("ticket_type_id", t.ID().String()),
		attribute.String("event_id", t.EventID().String()),
	))
	defer span.End()

	created, err := d.next.Create(ctx, t)
	if err != nil {
		span.RecordError(err)
	}
	return created, err
}

// GetByID is on the booking hot path (BookingService.BookTicket
// derives event_id + price snapshot from the ticket_type lookup), so
// the span is load-bearing for tail-latency analysis — without it,
// the gap between "API received request" and "Lua deduct" would be
// invisible in Jaeger waterfalls.
func (d *ticketTypeRepositoryTracingDecorator) GetByID(ctx context.Context, id uuid.UUID) (domain.TicketType, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "GetTicketTypeByID", trace.WithAttributes(
		attribute.String("ticket_type_id", id.String()),
	))
	defer span.End()

	t, err := d.next.GetByID(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return t, err
}

func (d *ticketTypeRepositoryTracingDecorator) Delete(ctx context.Context, id uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "DeleteTicketType", trace.WithAttributes(
		attribute.String("ticket_type_id", id.String()),
	))
	defer span.End()

	err := d.next.Delete(ctx, id)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *ticketTypeRepositoryTracingDecorator) ListByEventID(ctx context.Context, eventID uuid.UUID) ([]domain.TicketType, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "ListTicketTypesByEventID", trace.WithAttributes(
		attribute.String("event_id", eventID.String()),
	))
	defer span.End()

	tt, err := d.next.ListByEventID(ctx, eventID)
	if err != nil {
		span.RecordError(err)
	} else {
		span.SetAttributes(attribute.Int("count", len(tt)))
	}
	return tt, err
}

func (d *ticketTypeRepositoryTracingDecorator) DecrementTicket(ctx context.Context, id uuid.UUID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "DecrementTicketType", trace.WithAttributes(
		attribute.String("ticket_type_id", id.String()),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	err := d.next.DecrementTicket(ctx, id, quantity)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *ticketTypeRepositoryTracingDecorator) IncrementTicket(ctx context.Context, id uuid.UUID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "IncrementTicketType", trace.WithAttributes(
		attribute.String("ticket_type_id", id.String()),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	err := d.next.IncrementTicket(ctx, id, quantity)
	if err != nil {
		span.RecordError(err)
	}
	return err
}

func (d *ticketTypeRepositoryTracingDecorator) SumAvailableByEventID(ctx context.Context, eventID uuid.UUID) (int, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "SumAvailableByEventIDTicketType", trace.WithAttributes(
		attribute.String("event_id", eventID.String()),
	))
	defer span.End()

	sum, err := d.next.SumAvailableByEventID(ctx, eventID)
	if err != nil {
		span.RecordError(err)
	} else {
		span.SetAttributes(attribute.Int("sum", sum))
	}
	return sum, err
}
