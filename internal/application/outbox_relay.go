package application

import (
	"context"
	"fmt"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/pkg/logger"
)

const (
	outboxPollInterval = 500 * time.Millisecond
	// outboxLockID is the Postgres advisory lock key used for leader
	// election across all OutboxRelay replicas. Defined once to avoid
	// the historical magic-number-in-two-places bug (M4).
	outboxLockID int64 = 1001
)

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
//
// The advisory lock is released via defer so ANY exit path — panic,
// ctx.Done, or unexpected return — releases it. Previously the Unlock
// only ran on the ctx.Done branch, which leaked the lock on panic or
// on a bug that returned from the function without hitting ctx.Done.
func (r *OutboxRelay) runWithBatchHook(ctx context.Context, batchFn func(context.Context) error) {
	log := logger.FromCtx(ctx)
	log.Infow("outbox relay started", "batch_size", r.batchSize)

	ticker := time.NewTicker(outboxPollInterval)
	defer ticker.Stop()

	// Always release the advisory lock on exit, regardless of how we got
	// here. Use context.Background() because the parent ctx may already
	// be cancelled when this defer fires during shutdown.
	defer func() {
		if err := r.mutex.Unlock(context.Background(), outboxLockID); err != nil {
			log.Errorw("outbox relay: failed to release advisory lock",
				"lock_id", outboxLockID, "error", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Infow("outbox relay stopped")
			return
		case <-ticker.C:
			// 1. Leader Election: Try to acquire Postgres Advisory Lock
			acquired, err := r.mutex.TryLock(ctx, outboxLockID)
			if err != nil {
				log.Errorw("outbox relay: failed to acquire lock",
					"lock_id", outboxLockID, "error", err)
				continue
			}

			if !acquired {
				// We are in standby mode. Keep quiet or log at debug level.
				continue
			}

			// 2. We are the Leader! Process the batch. Errors are logged
			// by the batchFn itself (or its tracing decorator, which also
			// records them on the span); the ticker continues either way.
			if err := batchFn(ctx); err != nil {
				log.Errorw("outbox relay: batch error", "error", err)
			}
		}
	}
}

// processBatch now returns an error so the tracing decorator can call
// span.RecordError / span.SetStatus with precise information. It returns
// the FIRST fatal-to-batch error (list failure); per-event publish or
// mark-processed errors are logged inline and do not abort the batch
// because the next tick will retry.
func (r *OutboxRelay) processBatch(ctx context.Context) error {
	log := logger.FromCtx(ctx)

	events, err := r.outboxRepo.ListPending(ctx, r.batchSize)
	if err != nil {
		log.Errorw("outbox relay: failed to list pending events", "error", err)
		return fmt.Errorf("outbox relay list pending: %w", err)
	}

	for _, e := range events {
		// Respect context cancellation mid-batch for responsive shutdown.
		if ctx.Err() != nil {
			return ctx.Err()
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

	return nil
}
