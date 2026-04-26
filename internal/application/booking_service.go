package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

const tracerName = "application/service"

type BookingService interface {
	BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) error
	GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error)
}

type bookingService struct {
	orderRepo     domain.OrderRepository
	inventoryRepo domain.InventoryRepository
}

// NewBookingService wires the BookingService. eventRepo / uow used to
// be parameters too, but PR 35 confirmed they were dead injection —
// BookTicket only touches Redis (the worker handles all DB work
// asynchronously) and GetBookingHistory only needs orderRepo.
func NewBookingService(orderRepo domain.OrderRepository, inventoryRepo domain.InventoryRepository) BookingService {
	return &bookingService{
		orderRepo:     orderRepo,
		inventoryRepo: inventoryRepo,
	}
}

func (s *bookingService) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize

	return s.orderRepo.ListOrders(ctx, pageSize, offset, status)
}

func (s *bookingService) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) error {
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
