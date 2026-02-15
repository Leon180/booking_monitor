package application

import (
	"context"
	"fmt"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/pkg/logger"
)

const tracerName = "application/service"

type BookingService interface {
	BookTicket(ctx context.Context, userID, eventID, quantity int) error
	GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]*domain.Order, int, error)
}

type bookingService struct {
	eventRepo domain.EventRepository
	orderRepo domain.OrderRepository
	uow       domain.UnitOfWork
}

func NewBookingService(eventRepo domain.EventRepository, orderRepo domain.OrderRepository, uow domain.UnitOfWork) BookingService {
	return &bookingService{
		eventRepo: eventRepo,
		orderRepo: orderRepo,
		uow:       uow,
	}
}

func (s *bookingService) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]*domain.Order, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize

	return s.orderRepo.ListOrders(ctx, pageSize, offset, status)
}

func (s *bookingService) BookTicket(ctx context.Context, userID, eventID, quantity int) error {
	log := logger.FromCtx(ctx)
	log.Infow("processing booking request",
		"user_id", userID,
		"event_id", eventID,
		"quantity", quantity,
	)

	// Business Logic: Validate Quantity
	if quantity < 1 || quantity > 10 {
		return fmt.Errorf("invalid quantity: must be between 1 and 10")
	}

	// 1. Transaction (Unit of Work)
	err := s.uow.Do(ctx, func(txCtx context.Context) error {
		// Re-fetch logger from txCtx just in case, though usually it's the same
		log := logger.FromCtx(txCtx)

		// 1.1 Fetch
		event, err := s.eventRepo.GetByID(txCtx, eventID)
		if err != nil {
			return fmt.Errorf("fetch event: %w", err)
		}

		// 1.2 Domain Logic (Lifecycle)
		if err := event.Deduct(quantity); err != nil {
			// e.g. Domain ErrSoldOut
			log.Warnw("domain logic failed", "error", err)
			return err
		}

		// 1.3 Persist
		if err := s.eventRepo.Update(txCtx, event); err != nil {
			return fmt.Errorf("update event: %w", err)
		}

		// Create Order
		order := &domain.Order{
			EventID:   eventID,
			UserID:    userID,
			Quantity:  quantity,
			Status:    domain.OrderStatusConfirmed,
			CreatedAt: time.Now(),
		}
		if err := s.orderRepo.Create(txCtx, order); err != nil {
			return fmt.Errorf("create order: %w", err)
		}

		log.Infow("booking successful", "order_id", order.ID)
		return nil
	})

	if err != nil {
		log.Errorw("booking failed", "error", err)
		return err
	}

	return nil
}
