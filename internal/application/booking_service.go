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
	// Phase 2: Redis-Only Implementation
	// We rely on Redis Atomic DECR for inventory management.
	// We DO NOT persist to Postgres in this phase to demonstrate raw throughput.

	// 1. Atomic Deduct from Redis
	success, err := s.inventoryRepo.DeductInventory(ctx, eventID, quantity)
	if err != nil {
		return fmt.Errorf("redis inventory error: %w", err)
	}

	if !success {
		return domain.ErrSoldOut
	}

	// 2. Success!
	// In Phase 3, we will push a message to Kafka here.
	return nil

	/*
		// OLD LOGIC (Phase 1: DB Transaction)
		// ...
	*/
}
