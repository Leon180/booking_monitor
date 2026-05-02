package saga

import (
	"context"
	"encoding/json"
	"fmt"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type Compensator interface {
	HandleOrderFailed(ctx context.Context, payload []byte) error
}

type compensator struct {
	inventoryRepo domain.InventoryRepository
	uow           application.UnitOfWork
	log           *mlog.Logger
}

// NewCompensator takes the logger as an explicit dependency rather
// than reaching for zap's globals, matching the pattern used by
// WorkerService and keeping tests deterministic (L7).
//
// All DB work happens inside uow.Do so the per-aggregate repos are
// resolved off the closure parameter; the Redis revert outside the
// tx still uses the long-lived inventoryRepo.
func NewCompensator(
	inventoryRepo domain.InventoryRepository,
	uow application.UnitOfWork,
	logger *mlog.Logger,
) Compensator {
	return &compensator{
		inventoryRepo: inventoryRepo,
		uow:           uow,
		log:           logger.With(mlog.String("component", "saga_compensator")),
	}
}

func (s *compensator) HandleOrderFailed(ctx context.Context, payload []byte) error {
	var event application.OrderFailedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.log.Error(ctx, "failed to unmarshal event",
			tag.Error(err),
			mlog.ByteString("payload", payload),
		)
		return fmt.Errorf("compensator.HandleOrderFailed unmarshal: %w", err)
	}

	s.log.Info(ctx, "rolling back inventory for failed order",
		tag.OrderID(event.OrderID),
		tag.EventID(event.EventID),
		tag.Quantity(event.Quantity),
	)

	// 1. Rollback PostgreSQL Inventory & Ensure Idempotency via UoW
	errUow := s.uow.Do(ctx, func(repos *application.Repositories) error {
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

		if err := repos.Order.MarkCompensated(ctx, event.OrderID); err != nil {
			return fmt.Errorf("orderRepo.MarkCompensated order_id=%s: %w", event.OrderID, err)
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
	//
	// B3 sharding: RevertInventory MUST hit the same shard the original
	// deduct landed in. For B3.1's INVENTORY_SHARDS=1 default that is
	// always shard 0 (only one shard). B3.2 will plumb the shard
	// through OrderCreatedEvent → OrderFailedEvent and pass it here.
	compensationID := fmt.Sprintf("order:%s", event.OrderID)
	// TODO(B3.2): replace with event.Shard once OrderFailedEvent carries
	// the shard id end-to-end (worker → outbox → Kafka → here). Until
	// then INVENTORY_SHARDS defaults to 1 and the only valid shard is 0.
	const b3_1ShardForN1 = 0
	if err := s.inventoryRepo.RevertInventory(ctx, event.EventID, b3_1ShardForN1, event.Quantity, compensationID); err != nil {
		s.log.Error(ctx, "failed to rollback Redis inventory",
			tag.OrderID(event.OrderID),
			tag.Error(err),
		)
		return fmt.Errorf("inventoryRepo.RevertInventory order_id=%s: %w", event.OrderID, err)
	}

	s.log.Info(ctx, "rollback successful", tag.OrderID(event.OrderID))
	return nil
}
