package application

import (
	"context"
	"fmt"
	"time"

	"booking_monitor/internal/domain"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const tracerName = "application/service"

type BookingService struct {
	eventRepo domain.EventRepository
	orderRepo domain.OrderRepository
	logger    *zap.SugaredLogger
}

func NewBookingService(eventRepo domain.EventRepository, orderRepo domain.OrderRepository, logger *zap.SugaredLogger) *BookingService {
	return &BookingService{
		eventRepo: eventRepo,
		orderRepo: orderRepo,
		logger:    logger,
	}
}

func (s *BookingService) BookTicket(ctx context.Context, userID, eventID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "BookTicket", trace.WithAttributes(
		attribute.Int("user_id", userID),
		attribute.Int("event_id", eventID),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	s.logger.Infow("processing booking request",
		"user_id", userID,
		"event_id", eventID,
		"quantity", quantity,
	)

	// Business Logic: Validate Quantity
	if quantity < 1 || quantity > 10 {
		return fmt.Errorf("invalid quantity: must be between 1 and 10")
	}

	// 1. Deduct Inventory (Atomic)
	if err := s.eventRepo.DeductInventory(ctx, eventID, quantity); err != nil {
		span.RecordError(err)
		if err == domain.ErrSoldOut {
			s.logger.Warnw("event sold out", "event_id", eventID)
			return err
		}
		s.logger.Errorw("failed to deduct inventory", "error", err)
		return fmt.Errorf("deduct inventory: %w", err)
	}

	// 2. Create Order
	order := &domain.Order{
		EventID:   eventID,
		UserID:    userID,
		Quantity:  quantity,
		Status:    domain.OrderStatusConfirmed,
		CreatedAt: time.Now(),
	}

	if err := s.orderRepo.Create(ctx, order); err != nil {
		span.RecordError(err)
		s.logger.Errorw("failed to create order", "error", err)
		// Note: In a real system, we'd need to roll back the inventory deduction here!
		// But since we did a direct DB decrement, we'd need a compensating action or wrap in TX.
		// For Stage 1 simplicity (and keeping similar behavior to original), we acknowledge this gap.
		// Or we could have used a Tx across both calls if we exposed Tx management.
		// For now, let's log critical error.
		return fmt.Errorf("create order: %w", err)
	}

	s.logger.Infow("booking successful", "order_id", order.ID)
	return nil
}
