package application

import (
	"context"
	"fmt"

	"booking_monitor/internal/domain"
)

type EventService interface {
	CreateEvent(ctx context.Context, name string, totalTickets int) (*domain.Event, error)
}

type eventService struct {
	repo          domain.EventRepository
	inventoryRepo domain.InventoryRepository
}

func NewEventService(repo domain.EventRepository, inventoryRepo domain.InventoryRepository) EventService {
	return &eventService{
		repo:          repo,
		inventoryRepo: inventoryRepo,
	}
}

func (s *eventService) CreateEvent(ctx context.Context, name string, totalTickets int) (*domain.Event, error) {
	if totalTickets <= 0 {
		return nil, fmt.Errorf("total tickets must be positive")
	}

	event := &domain.Event{
		Name:             name,
		TotalTickets:     totalTickets,
		AvailableTickets: totalTickets,
		Version:          0,
	}

	// 1. Create in DB (Source of Truth for Metadata)
	if err := s.repo.Create(ctx, event); err != nil {
		return nil, err
	}

	// 2. Set in Redis (Hot Inventory)
	if err := s.inventoryRepo.SetInventory(ctx, event.ID, totalTickets); err != nil {
		// Just log warning? Or fail? For now, let's fail to ensure consistency.
		return nil, fmt.Errorf("failed to set redis inventory: %w", err)
	}

	return event, nil
}
