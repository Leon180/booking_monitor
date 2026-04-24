package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type WorkerService interface {
	// Start runs the consumer loop until ctx is cancelled. Returns nil
	// on clean shutdown (ctx.Canceled), or a wrapped error when the
	// underlying queue cannot be set up or subscription fails. Callers
	// are expected to escalate a non-nil return to fx.Shutdowner so k8s
	// restarts the pod instead of silently serving bookings against a
	// dead consumer.
	Start(ctx context.Context) error
}

type workerService struct {
	queue      domain.OrderQueue
	orderRepo  domain.OrderRepository
	eventRepo  domain.EventRepository
	outboxRepo domain.OutboxRepository
	uow        domain.UnitOfWork
	metrics    WorkerMetrics
	logger     *mlog.Logger
}

func NewWorkerService(
	queue domain.OrderQueue,
	orderRepo domain.OrderRepository,
	eventRepo domain.EventRepository,
	outboxRepo domain.OutboxRepository,
	uow domain.UnitOfWork,
	metrics WorkerMetrics,
	logger *mlog.Logger,
) WorkerService {
	return &workerService{
		queue:      queue,
		orderRepo:  orderRepo,
		eventRepo:  eventRepo,
		outboxRepo: outboxRepo,
		uow:        uow,
		metrics:    metrics,
		logger:     logger.With(mlog.String("component", "worker_service")),
	}
}

func (s *workerService) Start(ctx context.Context) error {
	s.logger.Info(ctx, "Starting worker service...")

	// ensure group. A startup-time failure here means the stream /
	// consumer group can't be created — no point continuing; bubble
	// up so the process exits and k8s restarts us. ctx.Canceled during
	// startup is a shutdown race (SIGINT arrived before Subscribe), not
	// a fault — treat it like clean exit so we don't log a spurious
	// failure or burn an exit(1) restart.
	if err := s.queue.EnsureGroup(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("worker ensure group: %w", err)
	}

	// subscribe (blocking until ctx cancelled or persistent failure)
	err := s.queue.Subscribe(ctx, func(ctx context.Context, msg *domain.OrderMessage) error {
		return s.processMessage(ctx, msg)
	})

	// Clean shutdown is signalled via ctx.Canceled; anything else is a
	// real failure (connection lost permanently, auth drift, bounded
	// consecutive-error threshold tripped) and must propagate.
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("worker subscribe: %w", err)
	}
	return nil
}

// processMessage handles a single order message within a DB transaction.
func (s *workerService) processMessage(ctx context.Context, msg *domain.OrderMessage) error {
	// Record duration in defer so panics, early returns, and future
	// refactors that add new return paths cannot silently skip the
	// histogram. Go 1.14+ open-coded defer overhead is negligible
	// next to the DB transaction below.
	start := time.Now()
	defer func() {
		s.metrics.RecordProcessingDuration(time.Since(start))
	}()

	return s.uow.Do(ctx, func(txCtx context.Context) error {
		// 1. Double Check Inventory (Source of Truth)
		if err := s.eventRepo.DecrementTicket(txCtx, msg.EventID, msg.Quantity); err != nil {
			if errors.Is(err, domain.ErrSoldOut) {
				s.logger.Warn(txCtx, "Inventory conflict: Redis approved but DB sold out",
					tag.MsgID(msg.ID), tag.EventID(msg.EventID))
				s.metrics.RecordInventoryConflict()
				s.metrics.RecordOrderOutcome("sold_out")
				return err
			}
			s.logger.Error(txCtx, "Failed to decrement ticket in DB", tag.MsgID(msg.ID), tag.Error(err))
			s.metrics.RecordOrderOutcome("db_error")
			return err
		}

		order := &domain.Order{
			UserID:    msg.UserID,
			EventID:   msg.EventID,
			Quantity:  msg.Quantity,
			Status:    domain.OrderStatusPending,
			CreatedAt: time.Now(),
		}

		if err := s.orderRepo.Create(txCtx, order); err != nil {
			if errors.Is(err, domain.ErrUserAlreadyBought) {
				s.logger.Warn(txCtx, "Duplicate purchase blocked by DB constraint",
					tag.MsgID(msg.ID), tag.UserID(msg.UserID), tag.EventID(msg.EventID))
				s.metrics.RecordOrderOutcome("duplicate")
				return err
			}
			s.logger.Error(txCtx, "Failed to create order", tag.MsgID(msg.ID), tag.Error(err))
			s.metrics.RecordOrderOutcome("db_error")
			return err
		}

		// 3. Outbox Pattern. Marshal errors are theoretical for the
		// current *domain.Order shape (ints, string, time.Time, enum)
		// but we still surface them so a future field addition can't
		// ship a silent nil-payload outbox row.
		payload, err := json.Marshal(order)
		if err != nil {
			s.logger.Error(txCtx, "Failed to marshal order for outbox", tag.MsgID(msg.ID), tag.Error(err))
			s.metrics.RecordOrderOutcome("db_error")
			return fmt.Errorf("marshal outbox payload: %w", err)
		}
		outboxEvent := &domain.OutboxEvent{
			EventType: "order.created",
			Payload:   payload,
			Status:    "PENDING",
		}

		if err := s.outboxRepo.Create(txCtx, outboxEvent); err != nil {
			s.logger.Error(txCtx, "Failed to create outbox event", tag.MsgID(msg.ID), tag.Error(err))
			s.metrics.RecordOrderOutcome("db_error")
			return err
		}

		s.logger.Info(txCtx, "Order processed successfully with Outbox",
			tag.MsgID(msg.ID), tag.OrderID(order.ID))
		s.metrics.RecordOrderOutcome("success")
		return nil
	})
}
