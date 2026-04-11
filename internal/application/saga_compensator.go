package application

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"booking_monitor/internal/domain"
)

type SagaCompensator interface {
	HandleOrderFailed(ctx context.Context, payload []byte) error
}

type sagaCompensator struct {
	eventRepo     domain.EventRepository
	inventoryRepo domain.InventoryRepository
	orderRepo     domain.OrderRepository
	uow           domain.UnitOfWork
	log           *zap.SugaredLogger
}

// NewSagaCompensator takes the logger as an explicit dependency rather
// than reaching for the global zap.S(), matching the pattern used by
// WorkerService and keeping tests deterministic (L7).
func NewSagaCompensator(
	eventRepo domain.EventRepository,
	inventoryRepo domain.InventoryRepository,
	orderRepo domain.OrderRepository,
	uow domain.UnitOfWork,
	log *zap.SugaredLogger,
) SagaCompensator {
	return &sagaCompensator{
		eventRepo:     eventRepo,
		inventoryRepo: inventoryRepo,
		orderRepo:     orderRepo,
		uow:           uow,
		log:           log.With("component", "saga_compensator"),
	}
}

func (s *sagaCompensator) HandleOrderFailed(ctx context.Context, payload []byte) error {
	var event domain.OrderFailedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.log.Errorw("failed to unmarshal event", "error", err, "payload", string(payload))
		return fmt.Errorf("sagaCompensator.HandleOrderFailed unmarshal: %w", err)
	}

	s.log.Infow("rolling back inventory for failed order",
		"order_id", event.OrderID,
		"event_id", event.EventID,
		"quantity", event.Quantity,
	)

	// 1. Rollback PostgreSQL Inventory & Ensure Idempotency via UoW
	errUow := s.uow.Do(ctx, func(txCtx context.Context) error {
		order, err := s.orderRepo.GetByID(txCtx, event.OrderID)
		if err != nil {
			return fmt.Errorf("orderRepo.GetByID order_id=%d: %w", event.OrderID, err)
		}
		if order.Status == domain.OrderStatusCompensated {
			// Idempotent: already rolled back in DB
			return nil
		}

		if err := s.eventRepo.IncrementTicket(txCtx, event.EventID, event.Quantity); err != nil {
			return fmt.Errorf("eventRepo.IncrementTicket event_id=%d: %w", event.EventID, err)
		}

		if err := s.orderRepo.UpdateStatus(txCtx, event.OrderID, domain.OrderStatusCompensated); err != nil {
			return fmt.Errorf("orderRepo.UpdateStatus order_id=%d: %w", event.OrderID, err)
		}
		return nil
	})

	if errUow != nil {
		s.log.Errorw("failed to rollback DB inventory", "order_id", event.OrderID, "error", errUow)
		return errUow // Retry — already wrapped inside the closure
	}

	// 2. Rollback Redis Inventory (Hot path). Idempotency is enforced
	// by the Lua script via an EXISTS-then-SET guard on a compensation
	// key (see revert.lua for the crash-safety trade-off).
	compensationID := fmt.Sprintf("order:%d", event.OrderID)
	if err := s.inventoryRepo.RevertInventory(ctx, event.EventID, event.Quantity, compensationID); err != nil {
		s.log.Errorw("failed to rollback Redis inventory",
			"order_id", event.OrderID,
			"error", err,
		)
		return fmt.Errorf("inventoryRepo.RevertInventory order_id=%d: %w", event.OrderID, err)
	}

	s.log.Infow("rollback successful", "order_id", event.OrderID)
	return nil
}
