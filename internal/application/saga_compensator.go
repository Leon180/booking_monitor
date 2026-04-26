package application

import (
	"context"
	"encoding/json"
	"fmt"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type SagaCompensator interface {
	HandleOrderFailed(ctx context.Context, payload []byte) error
}

type sagaCompensator struct {
	inventoryRepo domain.InventoryRepository
	uow           UnitOfWork
	log           *mlog.Logger
}

// NewSagaCompensator takes the logger as an explicit dependency rather
// than reaching for zap's globals, matching the pattern used by
// WorkerService and keeping tests deterministic (L7).
//
// All DB work happens inside uow.Do so the per-aggregate repos are
// resolved off the closure parameter; the Redis revert outside the
// tx still uses the long-lived inventoryRepo.
func NewSagaCompensator(
	inventoryRepo domain.InventoryRepository,
	uow UnitOfWork,
	logger *mlog.Logger,
) SagaCompensator {
	return &sagaCompensator{
		inventoryRepo: inventoryRepo,
		uow:           uow,
		log:           logger.With(mlog.String("component", "saga_compensator")),
	}
}

func (s *sagaCompensator) HandleOrderFailed(ctx context.Context, payload []byte) error {
	var event domain.OrderFailedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.log.Error(ctx, "failed to unmarshal event",
			tag.Error(err),
			mlog.ByteString("payload", payload),
		)
		return fmt.Errorf("sagaCompensator.HandleOrderFailed unmarshal: %w", err)
	}

	s.log.Info(ctx, "rolling back inventory for failed order",
		tag.OrderID(event.OrderID),
		tag.EventID(event.EventID),
		tag.Quantity(event.Quantity),
	)

	// 1. Rollback PostgreSQL Inventory & Ensure Idempotency via UoW
	errUow := s.uow.Do(ctx, func(repos *Repositories) error {
		order, err := repos.Order.GetByID(ctx, event.OrderID)
		if err != nil {
			return fmt.Errorf("orderRepo.GetByID order_id=%s: %w", event.OrderID, err)
		}
		if order.Status() == domain.OrderStatusCompensated {
			// Idempotent: already rolled back in DB
			return nil
		}

		if err := repos.Event.IncrementTicket(ctx, event.EventID, event.Quantity); err != nil {
			return fmt.Errorf("eventRepo.IncrementTicket event_id=%s: %w", event.EventID, err)
		}

		if err := repos.Order.UpdateStatus(ctx, event.OrderID, domain.OrderStatusCompensated); err != nil {
			return fmt.Errorf("orderRepo.UpdateStatus order_id=%s: %w", event.OrderID, err)
		}
		return nil
	})

	if errUow != nil {
		s.log.Error(ctx, "failed to rollback DB inventory", tag.OrderID(event.OrderID), tag.Error(errUow))
		return errUow // Retry — already wrapped inside the closure
	}

	// 2. Rollback Redis Inventory (Hot path). Idempotency is enforced
	// by the Lua script via an EXISTS-then-SET guard on a compensation
	// key (see revert.lua for the crash-safety trade-off).
	compensationID := fmt.Sprintf("order:%s", event.OrderID)
	if err := s.inventoryRepo.RevertInventory(ctx, event.EventID, event.Quantity, compensationID); err != nil {
		s.log.Error(ctx, "failed to rollback Redis inventory",
			tag.OrderID(event.OrderID),
			tag.Error(err),
		)
		return fmt.Errorf("inventoryRepo.RevertInventory order_id=%s: %w", event.OrderID, err)
	}

	s.log.Info(ctx, "rollback successful", tag.OrderID(event.OrderID))
	return nil
}
