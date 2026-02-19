package payment

import (
	"context"

	"go.uber.org/zap"

	"booking_monitor/internal/domain"
)

type Service struct {
	gateway   domain.PaymentGateway
	orderRepo domain.OrderRepository
	// publisher domain.EventPublisher // Future: for order.paid
	log *zap.SugaredLogger
}

func NewService(
	gateway domain.PaymentGateway,
	orderRepo domain.OrderRepository,
) domain.PaymentService {
	return &Service{
		gateway:   gateway,
		orderRepo: orderRepo,
		log:       zap.S().With("component", "payment_service"),
	}
}

func (s *Service) ProcessOrder(ctx context.Context, event *domain.OrderCreatedEvent) error {
	s.log.Infow("Processing payment for order", "order_id", event.OrderID, "amount", event.Amount)

	// 1. Idempotency Check
	// TODO: Implement domain.OrderRepository.GetByID to check if order is already paid.
	// For now, we rely on the happy path, which is acceptable for this phase.

	// 2. Call Payment Gateway
	if err := s.gateway.Charge(ctx, event.OrderID, event.Amount); err != nil {
		s.log.Errorw("Payment failed", "order_id", event.OrderID, "error", err)
		// Try to mark as failed
		_ = s.orderRepo.UpdateStatus(ctx, event.OrderID, domain.OrderStatusFailed)
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
