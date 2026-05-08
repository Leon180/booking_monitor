package worker

import (
	"context"
	"errors"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// MessageProcessor processes a single order message from the queue.
// Separated from Service so cross-cutting concerns (metrics,
// tracing) can be layered as decorators without the circular-callback
// problem of trying to decorate Service.Start directly — Start
// is a long-running loop that invokes processing internally via the
// queue's Subscribe handler, which would bypass any outer wrapper.
type MessageProcessor interface {
	Process(ctx context.Context, msg *QueuedBookingMessage) error
}

type orderMessageProcessor struct {
	uow    application.UnitOfWork
	logger *mlog.Logger
}

// NewOrderMessageProcessor returns the base (undecorated) processor.
// Consumers should typically use the version wrapped with
// NewMessageProcessorMetricsDecorator — see module.go wiring.
//
// All repo work happens inside uow.Do, so the per-repo dependencies
// are accessed via the closure's `repos *Repositories` parameter
// rather than fields on the processor struct. This keeps the tx
// boundary explicit at the call site (no field access can accidentally
// bypass it).
func NewOrderMessageProcessor(uow application.UnitOfWork, logger *mlog.Logger) MessageProcessor {
	return &orderMessageProcessor{
		uow:    uow,
		logger: logger.With(mlog.String("component", "message_processor")),
	}
}

// Process handles a single order message within a DB transaction.
// Returns raw domain errors so decorators (metrics, tracing) can
// classify outcome via errors.Is. Callers that need to know outcome
// category should NOT parse the error string — use errors.Is against
// domain.ErrSoldOut / domain.ErrUserAlreadyBought / domain.ErrInvalid*.
//
// D3 (Pattern A): the worker now constructs a Pattern A reservation
// (status=AwaitingPayment, reservedUntil set) via
// `domain.NewReservation`. BookingService already validated upstream;
// re-validating here is defense-in-depth — a malformed message that
// somehow slipped past parseMessage still trips a sentinel and the
// retry classifier short-circuits to DLQ. The legacy A4 charging
// flow is gone for new bookings; payment is now triggered by the D4
// `POST /api/v1/orders/:id/pay` endpoint.
func (p *orderMessageProcessor) Process(ctx context.Context, msg *QueuedBookingMessage) error {
	// Validate BEFORE opening a tx. A malformed queue message will
	// never become valid via PEL retry — failing fast saves a DB
	// transaction and lets the metrics decorator classify it as
	// "malformed_message" instead of "db_error". The Redis-side
	// inventory revert still happens on the worker's compensation
	// path (handleFailure -> RevertInventory).
	// Reuse the caller-minted OrderID from the queue message — same id
	// the API handler returned to the client at HTTP 202. Across PEL
	// retries this is stable; pre-PR-47 the worker minted a fresh
	// uuid per redelivery so the client's id and DB's id diverged on
	// retry.
	newOrder, err := domain.NewReservation(msg.OrderID, msg.UserID, msg.EventID, msg.TicketTypeID, msg.Quantity, msg.ReservedUntil, msg.AmountCents, msg.Currency)
	if err != nil {
		p.logger.Error(ctx, "Malformed order message",
			tag.MsgID(msg.MessageID), tag.Error(err))
		return err
	}

	return p.uow.Do(ctx, func(repos *application.Repositories) error {
		// 1. Double-check inventory against the source of truth. Redis
		// already approved via Lua deduct; DB disagreement means the
		// Redis view is ahead of DB (compensation path will fix it).
		//
		// D4.1 follow-up: decrements the per-ticket-type counter
		// (`event_ticket_types.available_tickets`) instead of the legacy
		// `events.available_tickets`. The ticket_types row is the SoT
		// the API surfaces as `ticket_types[].available_tickets`; the
		// pre-fix path decremented `events` only, leaving the visible
		// counter frozen at totalTickets (Codex P1 — counter drift).
		// `events.available_tickets` is now frozen post-D4.1; a
		// follow-up migration removes the column entirely.
		if err := repos.TicketType.DecrementTicket(ctx, msg.TicketTypeID, msg.Quantity); err != nil {
			if errors.Is(err, domain.ErrTicketTypeSoldOut) {
				p.logger.Warn(ctx, "Inventory conflict: Redis approved but ticket_type sold out in DB",
					tag.MsgID(msg.MessageID), tag.EventID(msg.EventID), tag.TicketTypeID(msg.TicketTypeID))
				return err
			}
			p.logger.Error(ctx, "Failed to decrement ticket_type in DB",
				tag.MsgID(msg.MessageID), tag.TicketTypeID(msg.TicketTypeID), tag.Error(err))
			return err
		}

		created, err := repos.Order.Create(ctx, newOrder)
		if err != nil {
			if errors.Is(err, domain.ErrUserAlreadyBought) {
				p.logger.Warn(ctx, "Duplicate purchase blocked by DB constraint",
					tag.MsgID(msg.MessageID), tag.UserID(msg.UserID), tag.EventID(msg.EventID))
				return err
			}
			p.logger.Error(ctx, "Failed to create order",
				tag.MsgID(msg.MessageID), tag.Error(err))
			return err
		}

		// D7 (2026-05-08): the legacy `events_outbox(event_type=
		// order.created)` write was removed here. Pre-D7 the
		// payment_worker consumed `order.created` to drive
		// gateway.Charge; D7 deleted that path entirely. Pattern A
		// drives money movement through `/api/v1/orders/:id/pay` (D4)
		// + the provider webhook (D5), neither of which depends on
		// `order.created`. The booking UoW is now `[INSERT order]`
		// only — single SQL statement, no fanout.
		p.logger.Info(ctx, "Order processed successfully",
			tag.MsgID(msg.MessageID), tag.OrderID(created.ID()))
		return nil
	})
}

