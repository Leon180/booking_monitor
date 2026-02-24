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

func NewSagaCompensator(
	eventRepo domain.EventRepository,
	inventoryRepo domain.InventoryRepository,
	orderRepo domain.OrderRepository,
	uow domain.UnitOfWork,
) SagaCompensator {
	return &sagaCompensator{
		eventRepo:     eventRepo,
		inventoryRepo: inventoryRepo,
		orderRepo:     orderRepo,
		uow:           uow,
		log:           zap.S().With("component", "saga_compensator"),
	}
}

func (s *sagaCompensator) HandleOrderFailed(ctx context.Context, payload []byte) error {
	var event domain.OrderFailedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.log.Errorw("failed to unmarshal event", "error", err, "payload", string(payload))
		return err
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
			return err
		}
		if order.Status == domain.OrderStatusCompensated {
			// Idempotent: already rolled back in DB
			return nil
		}

		if err := s.eventRepo.IncrementTicket(txCtx, event.EventID, event.Quantity); err != nil {
			return err
		}

		return s.orderRepo.UpdateStatus(txCtx, event.OrderID, domain.OrderStatusCompensated)
	})

	if errUow != nil {
		s.log.Errorw("failed to rollback DB inventory", "order_id", event.OrderID, "error", errUow)
		return errUow // Retry
	}

	// 2. Rollback Redis Inventory (Hot path)
	// Idempotency is enforced by the Lua script via a SETNX on a compensation key.
	compensationID := fmt.Sprintf("order:%d", event.OrderID)
	if err := s.inventoryRepo.RevertInventory(ctx, event.EventID, event.Quantity, compensationID); err != nil {
		s.log.Errorw("failed to rollback Redis inventory",
			"order_id", event.OrderID,
			"error", err,
		)
		return err
	}

	s.log.Infow("rollback successful", "order_id", event.OrderID)
	return nil
}
