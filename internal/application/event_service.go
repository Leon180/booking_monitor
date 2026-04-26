package application

import (
	"context"
	"fmt"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type EventService interface {
	CreateEvent(ctx context.Context, name string, totalTickets int) (domain.Event, error)
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

func (s *eventService) CreateEvent(ctx context.Context, name string, totalTickets int) (domain.Event, error) {
	// Invariant validation lives in domain.NewEvent — caller can
	// errors.Is against domain.ErrInvalidEventName / ErrInvalidTotalTickets.
	// Factory generates the UUID v7 id at construction.
	ev, err := domain.NewEvent(name, totalTickets)
	if err != nil {
		return domain.Event{}, err
	}

	created, err := s.repo.Create(ctx, ev)
	if err != nil {
		return domain.Event{}, fmt.Errorf("eventService.CreateEvent db create: %w", err)
	}

	// Set in Redis (Hot Inventory). If this fails after the DB row is
	// committed, compensate by deleting the DB row — otherwise the
	// event exists in Postgres but has no Redis inventory and is
	// permanently unsellable (the booking hot path reads from Redis).
	if err := s.inventoryRepo.SetInventory(ctx, created.ID(), totalTickets); err != nil {
		if delErr := s.repo.Delete(ctx, created.ID()); delErr != nil {
			// Compensation failed — dangling event row in DB with no
			// Redis inventory. Surface BOTH errors for manual recon.
			s.log.Error(ctx, "COMPENSATION FAILED — dangling event row",
				tag.EventID(created.ID()),
				mlog.NamedError("redis_error", err),
				mlog.NamedError("delete_error", delErr),
			)
			return domain.Event{}, fmt.Errorf("eventService.CreateEvent: redis SetInventory failed (%v) AND compensating DB delete failed: %w", err, delErr)
		}
		s.log.Warn(ctx, "compensated dangling event after Redis failure",
			tag.EventID(created.ID()), tag.Error(err))
		return domain.Event{}, fmt.Errorf("eventService.CreateEvent redis SetInventory: %w", err)
	}

	return created, nil
}
