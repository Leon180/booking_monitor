package payment

import (
	"context"
	"encoding/json"
	"fmt"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type Service struct {
	gateway    domain.PaymentGateway
	orderRepo  domain.OrderRepository
	outboxRepo domain.OutboxRepository
	uow        domain.UnitOfWork
	log        *mlog.Logger
}

// NewService takes the logger as an explicit dependency instead of
// reaching for zap's globals — that way fx can guarantee the logger is
// initialised before ProcessOrder starts handling Kafka messages.
func NewService(
	gateway domain.PaymentGateway,
	orderRepo domain.OrderRepository,
	outboxRepo domain.OutboxRepository,
	uow domain.UnitOfWork,
	logger *mlog.Logger,
) domain.PaymentService {
	return &Service{
		gateway:    gateway,
		orderRepo:  orderRepo,
		outboxRepo: outboxRepo,
		uow:        uow,
		log:        logger.With(mlog.String("component", "payment_service")),
	}
}

func (s *Service) ProcessOrder(ctx context.Context, event *domain.OrderCreatedEvent) error {
	s.log.Info(ctx, "Processing payment for order",
		tag.OrderID(event.OrderID), tag.Amount(event.Amount))

	// 1. Input Validation — return ErrInvalidPaymentEvent (NOT nil) so the
	// Kafka consumer can route the message to DLQ, emit a metric, and
	// commit the offset. Previously this function returned nil, which the
	// consumer interpreted as "success, commit offset" — permanently
	// losing the event without any operator-visible signal.
	if event.OrderID <= 0 {
		s.log.Error(ctx, "Invalid OrderID", tag.OrderID(event.OrderID))
		return fmt.Errorf("order_id=%d: %w", event.OrderID, domain.ErrInvalidPaymentEvent)
	}
	if event.Amount < 0 {
		s.log.Error(ctx, "Invalid Amount",
			tag.OrderID(event.OrderID), tag.Amount(event.Amount))
		return fmt.Errorf("amount=%v: %w", event.Amount, domain.ErrInvalidPaymentEvent)
	}

	// 2. Idempotency Check
	order, err := s.orderRepo.GetByID(ctx, event.OrderID)
	if err != nil {
		s.log.Error(ctx, "Failed to get order for idempotency check", tag.Error(err))
		return err
	}
	if order.Status != domain.OrderStatusPending {
		s.log.Info(ctx, "Order already processed, skipping payment",
			tag.OrderID(event.OrderID), tag.Status(string(order.Status)))
		return nil
	}

	// 3. Call Payment Gateway
	if err := s.gateway.Charge(ctx, event.OrderID, event.Amount); err != nil {
		s.log.Error(ctx, "Payment failed, initiating Saga Rollback",
			tag.OrderID(event.OrderID), tag.Error(err))

		// Create Saga compensating event (order.failed) atomically with status update
		errUow := s.uow.Do(ctx, func(txCtx context.Context) error {
			if updateErr := s.orderRepo.UpdateStatus(txCtx, event.OrderID, domain.OrderStatusFailed); updateErr != nil {
				return updateErr
			}

			// Map directly off the inbound OrderCreatedEvent — payment
			// service consumes an event from Kafka, it doesn't have a
			// fresh domain.Order at hand. NewOrderFailedEvent lives
			// next to OrderCreatedEvent so the messaging contract is
			// in one place (see internal/domain/order_events.go).
			failedEvent := domain.NewOrderFailedEvent(*event, err.Error())
			payload, marshalErr := json.Marshal(failedEvent)
			if marshalErr != nil {
				return marshalErr
			}

			_, createErr := s.outboxRepo.Create(txCtx, domain.NewOrderFailedOutbox(payload))
			return createErr
		})
		if errUow != nil {
			s.log.Error(ctx, "Failed to save saga compensating event",
				tag.OrderID(event.OrderID), tag.Error(errUow))
			return errUow // Return error to trigger Kafka retry
		}

		return nil // Consume event (don't retry indefinitely for business failures)
	}

	// 3. Update Order Status
	if err := s.orderRepo.UpdateStatus(ctx, event.OrderID, domain.OrderStatusConfirmed); err != nil {
		s.log.Error(ctx, "Failed to update order status to confirmed", tag.Error(err))
		return err // Retry update
	}

	s.log.Info(ctx, "Payment successful", tag.OrderID(event.OrderID))
	return nil
}
