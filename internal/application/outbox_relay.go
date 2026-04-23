package application

import (
	"context"
	"fmt"
	"time"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
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
	log        *mlog.Logger
}

func NewOutboxRelay(outboxRepo domain.OutboxRepository, publisher domain.EventPublisher, batchSize int, mutex domain.DistributedLock, logger *mlog.Logger) *OutboxRelay {
	if batchSize <= 0 {
		batchSize = 100
	}
	return &OutboxRelay{
		outboxRepo: outboxRepo,
		publisher:  publisher,
		batchSize:  batchSize,
		mutex:      mutex,
		log:        logger.With(mlog.String("component", "outbox_relay")),
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
	r.log.Info(ctx, "outbox relay started", mlog.Int("batch_size", r.batchSize))

	ticker := time.NewTicker(outboxPollInterval)
	defer ticker.Stop()

	// Track whether we ever acquired the lock so the defer doesn't
	// issue a spurious Unlock on a DistributedLock implementation that
	// lacks a nil-guard (pgAdvisoryLock is safe, but the interface
	// contract doesn't guarantee it).
	wasLeader := false

	// Release the advisory lock on exit. Use context.Background()
	// because the parent ctx may already be cancelled during shutdown.
	defer func() {
		if !wasLeader {
			return
		}
		unlockCtx := context.Background()
		if err := r.mutex.Unlock(unlockCtx, outboxLockID); err != nil {
			r.log.Error(ctx, "outbox relay: failed to release advisory lock",
				tag.LockID(outboxLockID), tag.Error(err))
		}
	}()

	for {
		select {
		case <-ctx.Done():
			r.log.Info(ctx, "outbox relay stopped")
			return
		case <-ticker.C:
			// 1. Leader Election: Try to acquire Postgres Advisory Lock
			acquired, err := r.mutex.TryLock(ctx, outboxLockID)
			if err != nil {
				r.log.Error(ctx, "outbox relay: failed to acquire lock",
					tag.LockID(outboxLockID), tag.Error(err))
				continue
			}

			if !acquired {
				// We are in standby mode. Keep quiet or log at debug level.
				continue
			}
			wasLeader = true

			// 2. We are the Leader! Process the batch. Errors are logged
			// by the batchFn itself (or its tracing decorator, which also
			// records them on the span); the ticker continues either way.
			if err := batchFn(ctx); err != nil {
				r.log.Error(ctx, "outbox relay: batch error", tag.Error(err))
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
	events, err := r.outboxRepo.ListPending(ctx, r.batchSize)
	if err != nil {
		r.log.Error(ctx, "outbox relay: failed to list pending events", tag.Error(err))
		return fmt.Errorf("outbox relay list pending: %w", err)
	}

	for _, e := range events {
		// Respect context cancellation mid-batch for responsive shutdown.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := r.publisher.Publish(ctx, e.EventType, e.Payload); err != nil {
			r.log.Error(ctx, "outbox relay: failed to publish event",
				mlog.Int("event_id", e.ID),
				tag.Topic(e.EventType),
				tag.Error(err),
			)
			// Intentional: do NOT mark as processed on publish failure.
			// The event will be retried on the next tick (at-least-once delivery).
			continue
		}

		if err := r.outboxRepo.MarkProcessed(ctx, e.ID); err != nil {
			r.log.Error(ctx, "outbox relay: failed to mark event as processed",
				mlog.Int("event_id", e.ID),
				tag.Error(err),
			)
			// Intentional: the event was already published. If MarkProcessed
			// fails, it will be re-published on the next tick. Consumers must
			// be idempotent (at-least-once delivery guarantee).
		}
	}

	return nil
}
