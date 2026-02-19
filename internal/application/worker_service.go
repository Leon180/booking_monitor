package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"booking_monitor/internal/domain"

	"go.uber.org/zap"
)

type WorkerService interface {
	Start(ctx context.Context)
}

type workerService struct {
	queue      domain.OrderQueue
	orderRepo  domain.OrderRepository
	eventRepo  domain.EventRepository
	outboxRepo domain.OutboxRepository
	uow        domain.UnitOfWork
	metrics    domain.WorkerMetrics
	logger     *zap.SugaredLogger
}

func NewWorkerService(
	queue domain.OrderQueue,
	orderRepo domain.OrderRepository,
	eventRepo domain.EventRepository,
	outboxRepo domain.OutboxRepository,
	uow domain.UnitOfWork,
	metrics domain.WorkerMetrics,
	logger *zap.SugaredLogger,
) WorkerService {
	return &workerService{
		queue:      queue,
		orderRepo:  orderRepo,
		eventRepo:  eventRepo,
		outboxRepo: outboxRepo,
		uow:        uow,
		metrics:    metrics,
		logger:     logger,
	}
}

func (s *workerService) Start(ctx context.Context) {
	s.logger.Info("Starting worker service...")

	// ensure group
	if err := s.queue.EnsureGroup(ctx); err != nil {
		s.logger.Errorw("Failed to ensure consumer group", "error", err)
		return
	}

	// subscribe (blocking)
	err := s.queue.Subscribe(ctx, func(ctx context.Context, msg *domain.OrderMessage) error {
		return s.processMessage(ctx, msg)
	})

	if err != nil && err != context.Canceled {
		s.logger.Errorw("Worker subscription failed", "error", err)
	}
}

// processMessage handles a single order message within a DB transaction.
func (s *workerService) processMessage(ctx context.Context, msg *domain.OrderMessage) error {
	start := time.Now()

	err := s.uow.Do(ctx, func(txCtx context.Context) error {
		// 1. Double Check Inventory (Source of Truth)
		if err := s.eventRepo.DecrementTicket(txCtx, msg.EventID, msg.Quantity); err != nil {
			if errors.Is(err, domain.ErrSoldOut) {
				s.logger.Warnw("Inventory conflict: Redis approved but DB sold out", "msg_id", msg.ID, "event_id", msg.EventID)
				s.metrics.RecordInventoryConflict()
				s.metrics.RecordOrderOutcome("sold_out")
				return err
			}
			s.logger.Errorw("Failed to decrement ticket in DB", "msg_id", msg.ID, "error", err)
			s.metrics.RecordOrderOutcome("db_error")
			return err
		}

		// 2. Create Order
		order := &domain.Order{
			UserID:    msg.UserID,
			EventID:   msg.EventID,
			Quantity:  msg.Quantity,
			Status:    "confirmed",
			CreatedAt: time.Now(),
		}

		if err := s.orderRepo.Create(txCtx, order); err != nil {
			if errors.Is(err, domain.ErrUserAlreadyBought) {
				s.logger.Warnw("Duplicate purchase blocked by DB constraint", "msg_id", msg.ID, "user_id", msg.UserID, "event_id", msg.EventID)
				s.metrics.RecordOrderOutcome("duplicate")
				return err
			}
			s.logger.Errorw("Failed to create order", "msg_id", msg.ID, "error", err)
			s.metrics.RecordOrderOutcome("db_error")
			return err
		}

		// 3. Outbox Pattern
		payload, _ := json.Marshal(order)
		outboxEvent := &domain.OutboxEvent{
			EventType: "order.created",
			Payload:   payload,
			Status:    "PENDING",
		}

		if err := s.outboxRepo.Create(txCtx, outboxEvent); err != nil {
			s.logger.Errorw("Failed to create outbox event", "msg_id", msg.ID, "error", err)
			s.metrics.RecordOrderOutcome("db_error")
			return err
		}

		s.logger.Infow("Order processed successfully with Outbox", "msg_id", msg.ID, "order_id", order.ID)
		s.metrics.RecordOrderOutcome("success")
		return nil
	})

	s.metrics.RecordProcessingDuration(time.Since(start))
	return err
}
