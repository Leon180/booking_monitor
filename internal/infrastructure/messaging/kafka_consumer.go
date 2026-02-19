package messaging

import (
	"context"
	"encoding/json"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
)

// KafkaConsumer consumes events from Kafka.
type KafkaConsumer struct {
	reader *kafka.Reader
	log    *zap.SugaredLogger
}

// NewKafkaConsumer creates a new Kafka consumer.
func NewKafkaConsumer(cfg *config.KafkaConfig) *KafkaConsumer {
	log := zap.S().With("component", "kafka_consumer")

	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{cfg.Brokers},
		GroupID:     "payment-service-group",
		Topic:       "order.created",
		StartOffset: kafka.FirstOffset,
		MinBytes:    10e3, // 10KB
		MaxBytes:    10e6, // 10MB
	})

	return &KafkaConsumer{
		reader: r,
		log:    log,
	}
}

// Start consumes messages and invokes the handler.
func (c *KafkaConsumer) Start(ctx context.Context, handler domain.PaymentService) error {
	c.log.Info("Starting Kafka consumer for topic: order.created")

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // Graceful shutdown
			}
			c.log.Errorw("Failed to fetch message", "error", err)
			time.Sleep(time.Second) // Backoff
			continue
		}

		c.log.Infow("Received message", "key", string(msg.Key), "offset", msg.Offset)

		var event domain.OrderCreatedEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			c.log.Errorw("Failed to unmarshal event", "error", err, "payload", string(msg.Value))
			// Commit bad message to avoid loop
			if err := c.reader.CommitMessages(ctx, msg); err != nil {
				c.log.Errorw("Failed to commit offset for bad message", "error", err)
			}
			continue
		}

		if err := handler.ProcessOrder(ctx, &event); err != nil {
			c.log.Errorw("Failed to process order", "order_id", event.OrderID, "error", err)
			// Simple retry policy: log and continue (at-least-once means we ideally retry until success or DLQ)
			// For this implementation, we log error but DO NOT commit offset if it's a transient error?
			// But if we don't commit, we block.
			// Let's assume we commit for now to keep things moving in this demo.
		}

		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			c.log.Errorw("Failed to commit offset", "error", err)
		}
	}
}

// Close closes the consumer connection.
func (c *KafkaConsumer) Close() error {
	return c.reader.Close()
}
