package messaging

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"

	"booking_monitor/internal/application/saga"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

const (
	// sagaDLQTopic is where saga events that exceed maxRetries are parked
	// for operator investigation. A separate consumer (future DLQ worker)
	// can drain this topic on a longer cadence.
	sagaDLQTopic = "order.failed.dlq"

	// sagaMaxRetries bounds the compensator retry budget per message.
	sagaMaxRetries = 3

	// sagaRetryKeyTTL keeps the Redis retry counter alive just long enough
	// to cover a partition rebalance / consumer restart; anything older
	// than this is stale and effectively dead-lettered.
	sagaRetryKeyTTL = 24 * time.Hour
)

// SagaConsumer consumes order.failed events from Kafka.
// It is part of the Booking Domain (Modular Monolith core).
//
// Retry accounting is persisted in Redis (not in-memory) so consumer
// restarts do not reset the counter. When a message exceeds the retry
// budget it is written to the DLQ topic, the counter is cleaned up, and
// the offset is committed — with a Prometheus metric so operators are
// paged instead of silently losing inventory.
//
// PR-D12.4 added the `metrics` field for the consumer-lag gauge +
// `lastMessageAt` for the idle-reset goroutine. The lag gauge is set
// per-FetchMessage as `time.Since(msg.Time)` (msg.Time was threaded
// from `events_outbox.created_at` by Slice 0). The idle-reset
// goroutine zeros the gauge if no message has been processed for >
// 30s — without this, a quiet system would keep the gauge stuck at
// the last value indefinitely, false-firing lag alerts.
//
// `lastMessageAt` is `atomic.Int64` storing UnixNano (rather than
// `atomic.Pointer[time.Time]`) — avoids a heap allocation per Store
// call (the &now indirection escape-analysis would force). 0 is the
// sentinel "never received a message" value. Slice 2 go-reviewer
// M-perf finding.
type SagaConsumer struct {
	reader        *kafka.Reader
	dlq           *kafka.Writer
	redis         *redis.Client
	log           *mlog.Logger
	metrics       saga.CompensatorMetrics
	lastMessageAt atomic.Int64 // UnixNano; 0 = never
}

// NewSagaConsumer constructs the consumer with a DLQ writer and a Redis
// client for durable retry counting. The logger is an explicit dependency
// so tests don't have to reach through zap's global logger.
//
// PR-D12.4: takes `saga.CompensatorMetrics` for the consumer-lag gauge
// + idle-reset goroutine. Tests can pass `saga.NopCompensatorMetrics{}`.
func NewSagaConsumer(cfg *config.KafkaConfig, rdb *redis.Client, logger *mlog.Logger, metrics saga.CompensatorMetrics) *SagaConsumer {
	scoped := logger.With(mlog.String("component", "saga_consumer"))

	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     cfg.Brokers,
		GroupID:     cfg.SagaGroupID, // Different group from payment!
		Topic:       cfg.OrderFailedTopic,
		StartOffset: kafka.FirstOffset,
		MinBytes:    10e3,
		MaxBytes:    10e6,
	})

	dlq := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Topic:                  sagaDLQTopic,
		Balancer:               &kafka.LeastBytes{},
		AllowAutoTopicCreation: true,
		WriteTimeout:           cfg.WriteTimeout,
	}

	return &SagaConsumer{
		reader:  r,
		dlq:     dlq,
		redis:   rdb,
		log:     scoped,
		metrics: metrics,
	}
}

// idleResetThreshold is how long the consumer can go without
// processing a message before the lag gauge is zeroed. 30s is a
// generous bound vs Kafka's normal delivery cadence (sub-second
// when busy). Sustained quiet beyond this represents either no
// `order.failed` traffic OR a stalled consumer; either way the
// last-known lag is no longer meaningful.
//
// idleResetTickInterval is how often the goroutine checks
// `lastMessageAt`. 5s gives ~5s worst-case latency between "30s
// idle" and "gauge zeroed" — fast enough that quiet-system false
// positive lag alerts can't sustain through `for: 2m`.
const (
	idleResetThreshold    = 30 * time.Second
	idleResetTickInterval = 5 * time.Second
)

// runIdleReset is the goroutine spawned by Start that zeros the
// consumer-lag gauge when the consumer has been idle for longer
// than idleResetThreshold. The blocking `FetchMessage` in the main
// loop has no idle-iteration hook, so this companion goroutine is
// the only way to drive the gauge to zero on a quiet system.
//
// Exits on ctx.Done — same lifecycle as the main consumer loop.
func (c *SagaConsumer) runIdleReset(ctx context.Context) {
	ticker := time.NewTicker(idleResetTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if shouldResetLag(c.lastMessageAt.Load(), time.Now(), idleResetThreshold) {
				c.metrics.SetConsumerLag(0)
			}
		}
	}
}

// shouldResetLag is the pure decision function extracted from
// runIdleReset for unit testability. Returns true if the lag
// gauge should be zeroed because the consumer has been idle long
// enough that the last-known lag is no longer meaningful.
//
// `lastUnixNano == 0` is the sentinel "consumer hasn't received
// any message yet since process start" — treat as idle so the
// gauge reads 0 from boot until the first message lands.
//
// Otherwise the gauge resets when `now - last > threshold`.
// Lifted to a free function so tests can drive synthetic time
// without dependency-injecting a clock interface (which would be
// invasive for a single-call site).
//
// Type asymmetry: `lastUnixNano` is int64 because the call site
// reads it from `c.lastMessageAt.Load()` (atomic.Int64 — picked
// over atomic.Pointer[time.Time] to avoid a heap allocation per
// Store on the hot Kafka loop). `now` and `threshold` are the
// natural types for the comparison. Conversion happens here, in
// one place, rather than at every Load() call site.
func shouldResetLag(lastUnixNano int64, now time.Time, threshold time.Duration) bool {
	if lastUnixNano == 0 {
		return true
	}
	return now.Sub(time.Unix(0, lastUnixNano)) > threshold
}

// Start blocks the calling goroutine until ctx is cancelled. The
// idle-reset companion goroutine spawned at the top is bounded by
// the SAME ctx — callers MUST cancel ctx for graceful shutdown to
// avoid a goroutine leak. Calling Start more than once on the same
// SagaConsumer instance spawns a duplicate idle-reset goroutine
// per call (production wiring calls Start exactly once via fx;
// tests should reuse the canonical pattern).
//
// PR-D12.4 added the lag-gauge update + idle-reset goroutine.
func (c *SagaConsumer) Start(ctx context.Context, compensator saga.Compensator) error {
	c.log.Info(ctx, "Starting Saga Consumer for topic: order.failed")

	// PR-D12.4: companion goroutine zeros the consumer-lag gauge
	// after idleResetThreshold of no-messages. Without it the gauge
	// sticks at the last value forever on a quiet system.
	go c.runIdleReset(ctx)

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // Graceful shutdown
			}
			c.log.Error(ctx, "failed to fetch message", tag.Error(err))
			time.Sleep(time.Second) // Backoff
			continue
		}

		// PR-D12.4 lag gauge: msg.Time was set by the outbox relay
		// to events_outbox.created_at (Slice 0 data-path). The lag
		// is the wall-clock between that moment and now — covers
		// outbox poll delay + Kafka round-trip + queue wait.
		c.lastMessageAt.Store(time.Now().UnixNano())
		// Clamp at zero: Postgres NOW() and consumer wall-clock can
		// diverge on a non-clock-synced fleet (Slice 2 silent-
		// failure-hunter L2). A negative `time.Since` would set a
		// nonsense gauge value; clamping makes "consumer ahead of
		// PG" indistinguishable from "fully caught up" — both are
		// "no actionable lag" from an operator's perspective.
		lag := time.Since(msg.Time)
		if lag < 0 {
			lag = 0
		}
		c.metrics.SetConsumerLag(lag)

		c.log.Info(ctx, "received failed order event", tag.Offset(msg.Offset))

		// Pass msg.Time as the histogram's start point —
		// events_outbox.created_at threaded through here so
		// ObserveLoopDuration measures end-to-end saga latency
		// (outbox poll delay + Kafka + compensator work), not just
		// the compensator's own execution time. Slice 0 set the
		// data path; this is the consumer-side fulfilment.
		if handleErr := compensator.HandleOrderFailed(ctx, msg.Value, msg.Time); handleErr != nil {
			c.log.Error(ctx, "failed to handle event", tag.Error(handleErr), tag.Offset(msg.Offset))

			count, incrErr := c.incrRetry(ctx, msg.Partition, msg.Offset)
			if incrErr != nil {
				// Redis is down — we cannot safely retry (no durable
				// counter) and we cannot safely drop (would be silent).
				// Do NOT commit so Kafka re-delivers on next rebalance.
				// Bump the retry counter so this "double-dependency
				// outage" (Kafka up + Redis down) is observable via
				// the KafkaConsumerStuck alert.
				observability.KafkaConsumerRetryTotal.
					WithLabelValues(msg.Topic, "transient_processing_error").Inc()
				c.log.Error(ctx, "failed to persist retry counter — will retry via rebalance",
					tag.Error(incrErr), tag.Offset(msg.Offset))
				continue
			}

			if count <= sagaMaxRetries {
				c.log.Warn(ctx, "compensation failed — retrying",
					mlog.Int64("attempt", count), tag.Offset(msg.Offset))
				continue // retry later (no commit)
			}

			// Budget exhausted — dead-letter first, bump metric ONLY on
			// successful write (the SagaConsumer.writeDLQ helper below
			// enforces this invariant; pre-D7 the legacy KafkaConsumer
			// for `order.created` shared the same shape).
			// If the DLQ write fails, we don't commit the
			// offset either — Kafka rebalance will redeliver and we'll
			// try again next time. This means poison messages may cause
			// a short hot-loop under DLQ failure, but at least the
			// metric never lies about what was actually dead-lettered.
			c.log.Error(ctx, "compensation max retries exceeded — dead-lettering",
				tag.Offset(msg.Offset),
				tag.Partition(msg.Partition),
				mlog.Int64("attempts", count),
			)
			if !c.writeDLQ(ctx, msg, handleErr) {
				// DLQ write failed — do NOT commit, do NOT clear the
				// retry counter. Next rebalance will retry.
				continue
			}
			observability.SagaPoisonMessagesTotal.Inc()
			observability.DLQMessagesTotal.WithLabelValues(sagaDLQTopic, "max_retries").Inc()
			_ = c.clearRetry(ctx, msg.Partition, msg.Offset)
			c.commitOrLog(ctx, msg)
			continue
		}

		// Success: clean up retry counter and commit offset.
		_ = c.clearRetry(ctx, msg.Partition, msg.Offset)
		c.commitOrLog(ctx, msg)
	}
}

// retryKey builds the Redis key that tracks how many times a given
// (partition, offset) has been retried. Separate partitions avoid
// cross-collision, and the offset is unique within a partition.
func (c *SagaConsumer) retryKey(partition int, offset int64) string {
	return fmt.Sprintf("saga:retry:p%d:o%d", partition, offset)
}

// incrRetry atomically increments the durable retry counter and resets
// its TTL. Returns the new count. Errors propagate so the caller can
// decide whether the message is safely dead-letterable.
func (c *SagaConsumer) incrRetry(ctx context.Context, partition int, offset int64) (int64, error) {
	key := c.retryKey(partition, offset)
	pipe := c.redis.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, sagaRetryKeyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("saga retry incr: %w", err)
	}
	return incr.Val(), nil
}

// clearRetry removes the durable retry counter. Best-effort — a stale
// counter expires via TTL if this fails.
func (c *SagaConsumer) clearRetry(ctx context.Context, partition int, offset int64) error {
	return c.redis.Del(ctx, c.retryKey(partition, offset)).Err()
}

// writeDLQ publishes the offending message to the saga DLQ topic with
// provenance headers so an operator (or a future DLQ worker) can triage.
// Returns true on successful publish, false on error. The caller uses
// the return value to decide whether to commit the Kafka offset — if
// the DLQ write failed, we must NOT commit, so rebalance will retry.
func (c *SagaConsumer) writeDLQ(ctx context.Context, msg kafka.Message, cause error) bool {
	dlqMsg := kafka.Message{
		Key:   msg.Key,
		Value: msg.Value,
		Headers: []kafka.Header{
			{Key: "x-original-topic", Value: []byte(msg.Topic)},
			{Key: "x-original-partition", Value: []byte(strconv.Itoa(msg.Partition))},
			{Key: "x-original-offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
			{Key: "x-dlq-reason", Value: []byte("max_retries")},
			{Key: "x-dlq-error", Value: []byte(cause.Error())},
		},
	}
	if err := c.dlq.WriteMessages(ctx, dlqMsg); err != nil {
		c.log.Error(ctx, "failed to write saga DLQ — message retained for retry",
			tag.Error(err), tag.Topic(sagaDLQTopic), tag.Offset(msg.Offset))
		return false
	}
	return true
}

func (c *SagaConsumer) commitOrLog(ctx context.Context, msg kafka.Message) {
	if err := c.reader.CommitMessages(ctx, msg); err != nil {
		c.log.Error(ctx, "failed to commit offset", tag.Error(err), tag.Offset(msg.Offset))
	}
}

func (c *SagaConsumer) Close() error {
	rErr := c.reader.Close()
	dErr := c.dlq.Close()
	if rErr != nil {
		return rErr
	}
	return dErr
}
