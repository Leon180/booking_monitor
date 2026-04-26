package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// MessageProcessor processes a single order message from the queue.
// Separated from WorkerService so cross-cutting concerns (metrics,
// tracing) can be layered as decorators without the circular-callback
// problem of trying to decorate WorkerService.Start directly — Start
// is a long-running loop that invokes processing internally via the
// queue's Subscribe handler, which would bypass any outer wrapper.
type MessageProcessor interface {
	Process(ctx context.Context, msg *QueuedBookingMessage) error
}

type orderMessageProcessor struct {
	uow    UnitOfWork
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
func NewOrderMessageProcessor(uow UnitOfWork, logger *mlog.Logger) MessageProcessor {
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
func (p *orderMessageProcessor) Process(ctx context.Context, msg *QueuedBookingMessage) error {
	// Validate BEFORE opening a tx. A malformed queue message will
	// never become valid via PEL retry — failing fast saves a DB
	// transaction and lets the metrics decorator classify it as
	// "malformed_message" instead of "db_error". The Redis-side
	// inventory revert still happens on the worker's compensation
	// path (handleFailure -> RevertInventory).
	newOrder, err := domain.NewOrder(msg.UserID, msg.EventID, msg.Quantity)
	if err != nil {
		p.logger.Error(ctx, "Malformed order message",
			tag.MsgID(msg.MessageID), tag.Error(err))
		return err
	}

	return p.uow.Do(ctx, func(repos *Repositories) error {
		// 1. Double-check inventory against the source of truth. Redis
		// already approved via Lua deduct; DB disagreement means the
		// Redis view is ahead of DB (compensation path will fix it).
		if err := repos.Event.DecrementTicket(ctx, msg.EventID, msg.Quantity); err != nil {
			if errors.Is(err, domain.ErrSoldOut) {
				p.logger.Warn(ctx, "Inventory conflict: Redis approved but DB sold out",
					tag.MsgID(msg.MessageID), tag.EventID(msg.EventID))
				return err
			}
			p.logger.Error(ctx, "Failed to decrement ticket in DB",
				tag.MsgID(msg.MessageID), tag.Error(err))
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

		// Outbox pattern. Marshal an explicit OrderCreatedEvent
		// payload — the wire contract with the payment consumer —
		// rather than json.Marshal(created) which would tie the
		// Kafka shape to the domain.Order struct. Marshal errors
		// are theoretical for the current event shape but we
		// surface them so a future field addition can't ship a
		// silent nil-payload outbox row.
		payload, err := json.Marshal(domain.NewOrderCreatedEvent(created))
		if err != nil {
			p.logger.Error(ctx, "Failed to marshal order_created event for outbox",
				tag.MsgID(msg.MessageID), tag.Error(err))
			return fmt.Errorf("marshal outbox payload: %w", err)
		}

		outboxEvent, err := domain.NewOrderCreatedOutbox(payload)
		if err != nil {
			p.logger.Error(ctx, "Failed to construct outbox event",
				tag.MsgID(msg.MessageID), tag.Error(err))
			return fmt.Errorf("construct outbox event: %w", err)
		}
		if _, err := repos.Outbox.Create(ctx, outboxEvent); err != nil {
			p.logger.Error(ctx, "Failed to create outbox event",
				tag.MsgID(msg.MessageID), tag.Error(err))
			return err
		}

		p.logger.Info(ctx, "Order processed successfully with Outbox",
			tag.MsgID(msg.MessageID), tag.OrderID(created.ID()))
		return nil
	})
}

