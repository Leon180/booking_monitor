package application

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type SagaCompensator interface {
	HandleOrderFailed(ctx context.Context, payload []byte) error
}

type sagaCompensator struct {
	eventRepo     domain.EventRepository
	inventoryRepo domain.InventoryRepository
	orderRepo     domain.OrderRepository
	uow           domain.UnitOfWork
	log           *mlog.Logger
}

// NewSagaCompensator takes the logger as an explicit dependency rather
// than reaching for zap's globals, matching the pattern used by
// WorkerService and keeping tests deterministic (L7).
func NewSagaCompensator(
	eventRepo domain.EventRepository,
	inventoryRepo domain.InventoryRepository,
	orderRepo domain.OrderRepository,
	uow domain.UnitOfWork,
	logger *mlog.Logger,
) SagaCompensator {
	return &sagaCompensator{
		eventRepo:     eventRepo,
		inventoryRepo: inventoryRepo,
		orderRepo:     orderRepo,
		uow:           uow,
		log:           logger.With(zap.String("component", "saga_compensator")),
	}
}

func (s *sagaCompensator) HandleOrderFailed(ctx context.Context, payload []byte) error {
	var event domain.OrderFailedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.log.L().Error("failed to unmarshal event",
			tag.Error(err),
			zap.ByteString("payload", payload),
		)
		return fmt.Errorf("sagaCompensator.HandleOrderFailed unmarshal: %w", err)
	}

	s.log.L().Info("rolling back inventory for failed order",
		tag.OrderID(event.OrderID),
		tag.EventID(event.EventID),
		tag.Quantity(event.Quantity),
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
		s.log.L().Error("failed to rollback DB inventory", tag.OrderID(event.OrderID), tag.Error(errUow))
		return errUow // Retry — already wrapped inside the closure
	}

	// 2. Rollback Redis Inventory (Hot path). Idempotency is enforced
	// by the Lua script via an EXISTS-then-SET guard on a compensation
	// key (see revert.lua for the crash-safety trade-off).
	compensationID := fmt.Sprintf("order:%d", event.OrderID)
	if err := s.inventoryRepo.RevertInventory(ctx, event.EventID, event.Quantity, compensationID); err != nil {
		s.log.L().Error("failed to rollback Redis inventory",
			tag.OrderID(event.OrderID),
			tag.Error(err),
		)
		return fmt.Errorf("inventoryRepo.RevertInventory order_id=%d: %w", event.OrderID, err)
	}

	s.log.L().Info("rollback successful", tag.OrderID(event.OrderID))
	return nil
}
