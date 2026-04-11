package messaging

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"

	"booking_monitor/internal/application"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
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
type SagaConsumer struct {
	reader *kafka.Reader
	dlq    *kafka.Writer
	redis  *redis.Client
	log    *zap.SugaredLogger
}

// NewSagaConsumer constructs the consumer with a DLQ writer and a Redis
// client for durable retry counting. All three collaborators are required.
func NewSagaConsumer(cfg *config.KafkaConfig, rdb *redis.Client) *SagaConsumer {
	log := zap.S().With("component", "saga_consumer")

	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{cfg.Brokers},
		GroupID:     cfg.SagaGroupID, // Different group from payment!
		Topic:       cfg.OrderFailedTopic,
		StartOffset: kafka.FirstOffset,
		MinBytes:    10e3,
		MaxBytes:    10e6,
	})

	dlq := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers),
		Topic:                  sagaDLQTopic,
		Balancer:               &kafka.LeastBytes{},
		AllowAutoTopicCreation: true,
		WriteTimeout:           cfg.WriteTimeout,
	}

	return &SagaConsumer{
		reader: r,
		dlq:    dlq,
		redis:  rdb,
		log:    log,
	}
}

func (c *SagaConsumer) Start(ctx context.Context, compensator application.SagaCompensator) error {
	c.log.Info("Starting Saga Consumer for topic: order.failed")

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // Graceful shutdown
			}
			c.log.Errorw("failed to fetch message", "error", err)
			time.Sleep(time.Second) // Backoff
			continue
		}

		c.log.Infow("received failed order event", "offset", msg.Offset)

		if handleErr := compensator.HandleOrderFailed(ctx, msg.Value); handleErr != nil {
			c.log.Errorw("failed to handle event", "error", handleErr, "offset", msg.Offset)

			count, incrErr := c.incrRetry(ctx, msg.Partition, msg.Offset)
			if incrErr != nil {
				// Redis is down — we cannot safely retry (no durable
				// counter) and we cannot safely drop (would be silent).
				// Do NOT commit so Kafka re-delivers on next rebalance.
				c.log.Errorw("failed to persist retry counter — will retry via rebalance",
					"error", incrErr, "offset", msg.Offset)
				continue
			}

			if count <= sagaMaxRetries {
				c.log.Warnw("compensation failed — retrying",
					"attempt", count, "offset", msg.Offset)
				continue // retry later (no commit)
			}

			// Budget exhausted — dead-letter, metric, commit.
			c.log.Errorw("compensation max retries exceeded — dead-lettering",
				"offset", msg.Offset, "partition", msg.Partition, "attempts", count)
			observability.SagaPoisonMessagesTotal.Inc()
			observability.DLQMessagesTotal.WithLabelValues(sagaDLQTopic, "max_retries").Inc()
			c.writeDLQ(ctx, msg, handleErr)
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
// Failures here are logged but do not block the consumer loop — the
// metric counter still fires so alerting remains accurate.
func (c *SagaConsumer) writeDLQ(ctx context.Context, msg kafka.Message, cause error) {
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
		c.log.Errorw("failed to write saga DLQ", "error", err, "topic", sagaDLQTopic)
	}
}

func (c *SagaConsumer) commitOrLog(ctx context.Context, msg kafka.Message) {
	if err := c.reader.CommitMessages(ctx, msg); err != nil {
		c.log.Errorw("failed to commit offset", "error", err, "offset", msg.Offset)
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
