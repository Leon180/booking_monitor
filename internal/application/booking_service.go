package application

import (
	"context"
	"fmt"

	"booking_monitor/internal/domain"
)

const tracerName = "application/service"

type BookingService interface {
	BookTicket(ctx context.Context, userID, eventID, quantity int) error
	GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]*domain.Order, int, error)
}

type bookingService struct {
	eventRepo     domain.EventRepository
	orderRepo     domain.OrderRepository
	inventoryRepo domain.InventoryRepository
	uow           domain.UnitOfWork
}

func NewBookingService(eventRepo domain.EventRepository, orderRepo domain.OrderRepository, inventoryRepo domain.InventoryRepository, uow domain.UnitOfWork) BookingService {
	return &bookingService{
		eventRepo:     eventRepo,
		orderRepo:     orderRepo,
		inventoryRepo: inventoryRepo,
		uow:           uow,
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
	// 1. Atomic Deduct from Redis (hot path — buyers set removed, no SISMEMBER)
	success, err := s.inventoryRepo.DeductInventory(ctx, eventID, userID, quantity)
	if err != nil {
		return fmt.Errorf("redis inventory error: %w", err)
	}

	if !success {
		return domain.ErrSoldOut
	}

	// 2. Success — order will be persisted async by the worker.
	// Duplicate purchase is enforced by DB UNIQUE(user_id, event_id) constraint.
	return nil
}
