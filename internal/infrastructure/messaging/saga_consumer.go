package messaging

import (
	"context"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"

	"booking_monitor/internal/application"
	"booking_monitor/internal/infrastructure/config"
)

// SagaConsumer consumes order.failed events from Kafka.
// It is part of the Booking Domain (Modular Monolith core).
type SagaConsumer struct {
	reader *kafka.Reader
	log    *zap.SugaredLogger
}

func NewSagaConsumer(cfg *config.KafkaConfig) *SagaConsumer {
	log := zap.S().With("component", "saga_consumer")

	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{cfg.Brokers},
		GroupID:     "booking-saga-group", // Different group from payment!
		Topic:       "order.failed",
		StartOffset: kafka.FirstOffset,
		MinBytes:    10e3,
		MaxBytes:    10e6,
	})

	return &SagaConsumer{
		reader: r,
		log:    log,
	}
}

func (c *SagaConsumer) Start(ctx context.Context, compensator application.SagaCompensator) error {
	c.log.Info("Starting Saga Consumer for topic: order.failed")

	// Poor-man's DLQ/Retry tracker for transient failures
	retries := make(map[int64]int)
	const maxRetries = 3

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

		if err := compensator.HandleOrderFailed(ctx, msg.Value); err != nil {
			c.log.Errorw("failed to handle event", "error", err, "offset", msg.Offset)

			retries[msg.Offset]++
			if retries[msg.Offset] > maxRetries {
				c.log.Warnw("max retries exceeded, skipping message to prevent partition blocking",
					"offset", msg.Offset,
					"partition", msg.Partition)
				// Clean up map and allow commit to bypass
				delete(retries, msg.Offset)
			} else {
				// At-least-once: retry later (no commit)
				continue
			}
		}

		// Message succeeded (or skipped). Clean up tracker.
		delete(retries, msg.Offset)

		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			c.log.Errorw("failed to commit offset", "error", err)
		}
	}
}

func (c *SagaConsumer) Close() error {
	return c.reader.Close()
}
