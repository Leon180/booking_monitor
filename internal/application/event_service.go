package application

import (
	"context"
	"fmt"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type EventService interface {
	CreateEvent(ctx context.Context, name string, totalTickets int) (*domain.Event, error)
}

type eventService struct {
	repo          domain.EventRepository
	inventoryRepo domain.InventoryRepository
	log           *mlog.Logger
}

// NewEventService takes the logger as an explicit dependency rather than
// reaching for zap's globals. This matches WorkerService /
// SagaCompensator and guarantees the logger is initialized by the time
// fx calls this constructor.
func NewEventService(repo domain.EventRepository, inventoryRepo domain.InventoryRepository, logger *mlog.Logger) EventService {
	return &eventService{
		repo:          repo,
		inventoryRepo: inventoryRepo,
		log:           logger.With(mlog.String("component", "event_service")),
	}
}

func (s *eventService) CreateEvent(ctx context.Context, name string, totalTickets int) (*domain.Event, error) {
	// Invariant validation now lives in domain.NewEvent — caller can
	// errors.Is against domain.ErrInvalidEventName / ErrInvalidTotalTickets.
	ev, err := domain.NewEvent(name, totalTickets)
	if err != nil {
		return nil, err
	}

	// EventRepository.Create still uses the pre-PR-30 pointer-write-back
	// pattern (the full migration to value-in / value-out is queued in
	// memory as A2 / the upcoming UUID v7 PR). Take address of the local
	// `ev` so the repo can scan ID into it; we then return the populated
	// pointer to the caller. This asymmetry with NewOrder/Create is
	// intentional and tracked.
	event := &ev
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
			s.log.Error(ctx, "COMPENSATION FAILED — dangling event row",
				tag.EventID(event.ID),
				mlog.NamedError("redis_error", err),
				mlog.NamedError("delete_error", delErr),
			)
			return nil, fmt.Errorf("eventService.CreateEvent: redis SetInventory failed (%v) AND compensating DB delete failed: %w", err, delErr)
		}
		s.log.Warn(ctx, "compensated dangling event after Redis failure",
			tag.EventID(event.ID), tag.Error(err))
		return nil, fmt.Errorf("eventService.CreateEvent redis SetInventory: %w", err)
	}

	return event, nil
}
