package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"booking_monitor/internal/domain"
)

type EventService interface {
	CreateEvent(ctx context.Context, name string, totalTickets int) (*domain.Event, error)
}

type eventService struct {
	repo          domain.EventRepository
	inventoryRepo domain.InventoryRepository
	log           *zap.SugaredLogger
}

// NewEventService takes the logger as an explicit dependency rather than
// reaching for the global zap.S(). This matches WorkerService /
// SagaCompensator and ensures the logger is initialized by the time fx
// calls this constructor (zap.ReplaceGlobals may not have fired yet).
func NewEventService(repo domain.EventRepository, inventoryRepo domain.InventoryRepository, log *zap.SugaredLogger) EventService {
	return &eventService{
		repo:          repo,
		inventoryRepo: inventoryRepo,
		log:           log.With("component", "event_service"),
	}
}

func (s *eventService) CreateEvent(ctx context.Context, name string, totalTickets int) (*domain.Event, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("event name must not be empty")
	}
	if totalTickets <= 0 {
		return nil, errors.New("total tickets must be positive")
	}

	event := &domain.Event{
		Name:             name,
		TotalTickets:     totalTickets,
		AvailableTickets: totalTickets,
		Version:          0,
	}

	// 1. Create in DB (Source of Truth for Metadata)
	if err := s.repo.Create(ctx, event); err != nil {
		return nil, fmt.Errorf("eventService.CreateEvent db create: %w", err)
	}

	// 2. Set in Redis (Hot Inventory). If this fails after the DB row is
	// committed, we must compensate by deleting the DB row — otherwise
	// the event exists in Postgres but has no Redis inventory and is
	// permanently unsellable (the booking hot path reads from Redis).
	if err := s.inventoryRepo.SetInventory(ctx, event.ID, totalTickets); err != nil {
		if delErr := s.repo.Delete(ctx, event.ID); delErr != nil {
			// Compensation failed — we now have a dangling event row
			// in the DB with no Redis inventory. Surface BOTH errors
			// so the operator can reconcile manually.
			s.log.Errorw("COMPENSATION FAILED — dangling event row",
				"event_id", event.ID,
				"redis_error", err,
				"delete_error", delErr)
			return nil, fmt.Errorf("eventService.CreateEvent: redis SetInventory failed (%v) AND compensating DB delete failed: %w", err, delErr)
		}
		s.log.Warnw("compensated dangling event after Redis failure",
			"event_id", event.ID, "error", err)
		return nil, fmt.Errorf("eventService.CreateEvent redis SetInventory: %w", err)
	}

	return event, nil
}
