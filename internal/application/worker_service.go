package application

import (
	"context"
	"errors"
	"fmt"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
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
	queue     domain.OrderQueue
	processor MessageProcessor
	logger    *mlog.Logger
}

// NewWorkerService wires a worker around a (typically metrics-decorated)
// MessageProcessor. The processor passed in is treated as an opaque
// handler — the worker is only responsible for queue lifecycle
// (EnsureGroup, Subscribe, ctx handling); per-message observability and
// processing logic live in the processor chain.
func NewWorkerService(queue domain.OrderQueue, processor MessageProcessor, logger *mlog.Logger) WorkerService {
	return &workerService{
		queue:     queue,
		processor: processor,
		logger:    logger.With(mlog.String("component", "worker_service")),
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

	// subscribe (blocking until ctx cancelled or persistent failure).
	// Handing processor.Process directly means the decorator chain
	// intercepts every message — if we inlined a closure that called
	// an unexported method, the decorator would be bypassed.
	err := s.queue.Subscribe(ctx, s.processor.Process)

	// Clean shutdown is signalled via ctx.Canceled; anything else is a
	// real failure (connection lost permanently, auth drift, bounded
	// consecutive-error threshold tripped) and must propagate.
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("worker subscribe: %w", err)
	}
	return nil
}
