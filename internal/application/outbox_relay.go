package application

import (
	"context"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/pkg/logger"
)

const outboxPollInterval = 500 * time.Millisecond

// OutboxRelay polls the outbox table and publishes pending events to the message bus.
// It runs as a background goroutine managed by the Fx lifecycle.
type OutboxRelay struct {
	outboxRepo domain.OutboxRepository
	publisher  domain.EventPublisher
	batchSize  int
	mutex      domain.DistributedLock
}

func NewOutboxRelay(outboxRepo domain.OutboxRepository, publisher domain.EventPublisher, batchSize int, mutex domain.DistributedLock) *OutboxRelay {
	if batchSize <= 0 {
		batchSize = 100
	}
	return &OutboxRelay{
		outboxRepo: outboxRepo,
		publisher:  publisher,
		batchSize:  batchSize,
		mutex:      mutex,
	}
}

// Run starts the relay loop. It blocks until ctx is cancelled.
func (r *OutboxRelay) Run(ctx context.Context) {
	r.runWithBatchHook(ctx, r.processBatch)
}

// runWithBatchHook runs the ticker loop, calling batchFn on each tick.
// This allows the tracing decorator to inject a traced version of processBatch.
func (r *OutboxRelay) runWithBatchHook(ctx context.Context, batchFn func(context.Context)) {
	log := logger.FromCtx(ctx)
	log.Infow("outbox relay started", "batch_size", r.batchSize)

	ticker := time.NewTicker(outboxPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Infow("outbox relay stopped")
			// Ensure lock is released on graceful shutdown
			_ = r.mutex.Unlock(context.Background(), 1001)
			return
		case <-ticker.C:
			// 1. Leader Election: Try to acquire Postgres Advisory Lock (LockID: 1001)
			acquired, err := r.mutex.TryLock(ctx, 1001)
			if err != nil {
				log.Errorw("outbox relay: failed to acquire lock", "error", err)
				continue
			}

			if !acquired {
				// We are in standby mode. Keep quiet or log at debug level.
				continue
			}

			// 2. We are the Leader! Process the batch.
			batchFn(ctx)
		}
	}
}

func (r *OutboxRelay) processBatch(ctx context.Context) {
	log := logger.FromCtx(ctx)

	events, err := r.outboxRepo.ListPending(ctx, r.batchSize)
	if err != nil {
		log.Errorw("outbox relay: failed to list pending events", "error", err)
		return
	}

	for _, e := range events {
		// Respect context cancellation mid-batch for responsive shutdown.
		if ctx.Err() != nil {
			return
		}

		if err := r.publisher.Publish(ctx, e.EventType, e.Payload); err != nil {
			log.Errorw("outbox relay: failed to publish event",
				"event_id", e.ID,
				"topic", e.EventType,
				"error", err,
			)
			// Intentional: do NOT mark as processed on publish failure.
			// The event will be retried on the next tick (at-least-once delivery).
			continue
		}

		if err := r.outboxRepo.MarkProcessed(ctx, e.ID); err != nil {
			log.Errorw("outbox relay: failed to mark event as processed",
				"event_id", e.ID,
				"error", err,
			)
			// Intentional: the event was already published. If MarkProcessed
			// fails, it will be re-published on the next tick. Consumers must
			// be idempotent (at-least-once delivery guarantee).
		}
	}
}
