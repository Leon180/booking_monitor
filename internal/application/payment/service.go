package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"booking_monitor/internal/domain"
)

type Service struct {
	gateway    domain.PaymentGateway
	orderRepo  domain.OrderRepository
	outboxRepo domain.OutboxRepository
	uow        domain.UnitOfWork
	log        *zap.SugaredLogger
}

func NewService(
	gateway domain.PaymentGateway,
	orderRepo domain.OrderRepository,
	outboxRepo domain.OutboxRepository,
	uow domain.UnitOfWork,
) domain.PaymentService {
	return &Service{
		gateway:    gateway,
		orderRepo:  orderRepo,
		outboxRepo: outboxRepo,
		uow:        uow,
		log:        zap.S().With("component", "payment_service"),
	}
}

func (s *Service) ProcessOrder(ctx context.Context, event *domain.OrderCreatedEvent) error {
	s.log.Infow("Processing payment for order", "order_id", event.OrderID, "amount", event.Amount)

	// 1. Input Validation — return ErrInvalidPaymentEvent (NOT nil) so the
	// Kafka consumer can route the message to DLQ, emit a metric, and
	// commit the offset. Previously this function returned nil, which the
	// consumer interpreted as "success, commit offset" — permanently
	// losing the event without any operator-visible signal.
	if event.OrderID <= 0 {
		s.log.Errorw("Invalid OrderID", "order_id", event.OrderID)
		return fmt.Errorf("order_id=%d: %w", event.OrderID, domain.ErrInvalidPaymentEvent)
	}
	if event.Amount < 0 {
		s.log.Errorw("Invalid Amount", "order_id", event.OrderID, "amount", event.Amount)
		return fmt.Errorf("amount=%v: %w", event.Amount, domain.ErrInvalidPaymentEvent)
	}

	// 2. Idempotency Check
	order, err := s.orderRepo.GetByID(ctx, event.OrderID)
	if err != nil {
		s.log.Errorw("Failed to get order for idempotency check", "error", err)
		return err
	}
	if order.Status != domain.OrderStatusPending {
		s.log.Infow("Order already processed, skipping payment", "order_id", event.OrderID, "status", order.Status)
		return nil
	}

	// 3. Call Payment Gateway
	if err := s.gateway.Charge(ctx, event.OrderID, event.Amount); err != nil {
		s.log.Errorw("Payment failed, initiating Saga Rollback", "order_id", event.OrderID, "error", err)

		// Create Saga compensating event (order.failed) atomically with status update
		errUow := s.uow.Do(ctx, func(txCtx context.Context) error {
			if updateErr := s.orderRepo.UpdateStatus(txCtx, event.OrderID, domain.OrderStatusFailed); updateErr != nil {
				return updateErr
			}

			failedEvent := &domain.OrderFailedEvent{
				EventID:  event.EventID,
				OrderID:  event.OrderID,
				UserID:   event.UserID,
				Quantity: event.Quantity,
				FailedAt: time.Now(),
				Reason:   err.Error(),
			}
			payload, marshalErr := json.Marshal(failedEvent)
			if marshalErr != nil {
				return marshalErr
			}

			outboxEvent := &domain.OutboxEvent{
				EventType: domain.EventTypeOrderFailed,
				Payload:   payload,
				Status:    domain.OutboxStatusPending,
			}
			return s.outboxRepo.Create(txCtx, outboxEvent)
		})
		if errUow != nil {
			s.log.Errorw("Failed to save saga compensating event", "order_id", event.OrderID, "error", errUow)
			return errUow // Return error to trigger Kafka retry
		}

		return nil // Consume event (don't retry indefinitely for business failures)
	}

	// 3. Update Order Status
	if err := s.orderRepo.UpdateStatus(ctx, event.OrderID, domain.OrderStatusConfirmed); err != nil {
		s.log.Errorw("Failed to update order status to confirmed", "error", err)
		return err // Retry update
	}

	s.log.Infow("Payment successful", "order_id", event.OrderID)
	return nil
}
