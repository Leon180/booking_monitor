package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	Process(ctx context.Context, msg *domain.OrderMessage) error
}

type orderMessageProcessor struct {
	orderRepo  domain.OrderRepository
	eventRepo  domain.EventRepository
	outboxRepo domain.OutboxRepository
	uow        domain.UnitOfWork
	logger     *mlog.Logger
}

// NewOrderMessageProcessor returns the base (undecorated) processor.
// Consumers should typically use the version wrapped with
// NewMessageProcessorMetricsDecorator — see module.go wiring.
func NewOrderMessageProcessor(
	orderRepo domain.OrderRepository,
	eventRepo domain.EventRepository,
	outboxRepo domain.OutboxRepository,
	uow domain.UnitOfWork,
	logger *mlog.Logger,
) MessageProcessor {
	return &orderMessageProcessor{
		orderRepo:  orderRepo,
		eventRepo:  eventRepo,
		outboxRepo: outboxRepo,
		uow:        uow,
		logger:     logger.With(mlog.String("component", "message_processor")),
	}
}

// Process handles a single order message within a DB transaction.
// Returns raw domain errors so decorators (metrics, tracing) can
// classify outcome via errors.Is. Callers that need to know outcome
// category should NOT parse the error string — use errors.Is against
// domain.ErrSoldOut / domain.ErrUserAlreadyBought.
func (p *orderMessageProcessor) Process(ctx context.Context, msg *domain.OrderMessage) error {
	return p.uow.Do(ctx, func(txCtx context.Context) error {
		// 1. Double-check inventory against the source of truth. Redis
		// already approved via Lua deduct; DB disagreement means the
		// Redis view is ahead of DB (compensation path will fix it).
		if err := p.eventRepo.DecrementTicket(txCtx, msg.EventID, msg.Quantity); err != nil {
			if errors.Is(err, domain.ErrSoldOut) {
				p.logger.Warn(txCtx, "Inventory conflict: Redis approved but DB sold out",
					tag.MsgID(msg.ID), tag.EventID(msg.EventID))
				return err
			}
			p.logger.Error(txCtx, "Failed to decrement ticket in DB",
				tag.MsgID(msg.ID), tag.Error(err))
			return err
		}

		order := &domain.Order{
			UserID:    msg.UserID,
			EventID:   msg.EventID,
			Quantity:  msg.Quantity,
			Status:    domain.OrderStatusPending,
			CreatedAt: time.Now(),
		}

		if err := p.orderRepo.Create(txCtx, order); err != nil {
			if errors.Is(err, domain.ErrUserAlreadyBought) {
				p.logger.Warn(txCtx, "Duplicate purchase blocked by DB constraint",
					tag.MsgID(msg.ID), tag.UserID(msg.UserID), tag.EventID(msg.EventID))
				return err
			}
			p.logger.Error(txCtx, "Failed to create order",
				tag.MsgID(msg.ID), tag.Error(err))
			return err
		}

		// Outbox pattern. Marshal errors are theoretical for the
		// current *domain.Order shape (ints, string, time.Time, enum)
		// but we still surface them so a future field addition can't
		// ship a silent nil-payload outbox row.
		payload, err := json.Marshal(order)
		if err != nil {
			p.logger.Error(txCtx, "Failed to marshal order for outbox",
				tag.MsgID(msg.ID), tag.Error(err))
			return fmt.Errorf("marshal outbox payload: %w", err)
		}

		outboxEvent := &domain.OutboxEvent{
			EventType: "order.created",
			Payload:   payload,
			Status:    "PENDING",
		}

		if err := p.outboxRepo.Create(txCtx, outboxEvent); err != nil {
			p.logger.Error(txCtx, "Failed to create outbox event",
				tag.MsgID(msg.ID), tag.Error(err))
			return err
		}

		p.logger.Info(txCtx, "Order processed successfully with Outbox",
			tag.MsgID(msg.ID), tag.OrderID(order.ID))
		return nil
	})
}

