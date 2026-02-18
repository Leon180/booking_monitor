package application

import (
	"context"
	"time"

	"booking_monitor/internal/domain"

	"go.uber.org/zap"
)

type WorkerService interface {
	Start(ctx context.Context)
}

type workerService struct {
	queue     domain.OrderQueue
	orderRepo domain.OrderRepository
	logger    *zap.SugaredLogger
}

func NewWorkerService(queue domain.OrderQueue, orderRepo domain.OrderRepository, logger *zap.SugaredLogger) WorkerService {
	return &workerService{
		queue:     queue,
		orderRepo: orderRepo,
		logger:    logger,
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
	// We run this in a goroutine typically, but here the Start method is called inside a goroutine in main.go
	// Wait, Start is blocking because Subscribe is blocking.
	err := s.queue.Subscribe(ctx, func(ctx context.Context, msg *domain.OrderMessage) error {
		order := &domain.Order{
			UserID:    msg.UserID,
			EventID:   msg.EventID,
			Quantity:  msg.Quantity,
			Status:    "confirmed", // String for now, domain uses string? Let's check.
			CreatedAt: time.Now(),
		}

		if err := s.orderRepo.Create(ctx, order); err != nil {
			s.logger.Errorw("Failed to create order", "msg_id", msg.ID, "error", err)
			return err
		}

		s.logger.Infow("Order processed successfully", "msg_id", msg.ID, "order_id", order.ID)
		return nil
	})

	if err != nil && err != context.Canceled {
		s.logger.Errorw("Worker subscription failed", "error", err)
	}
}
