package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
)

// paymentDLQTopic is where payment events that cannot be processed end up.
// These include malformed JSON and ErrInvalidPaymentEvent (bad input).
const paymentDLQTopic = "order.created.dlq"

// KafkaConsumer consumes events from Kafka and routes unprocessable
// messages (malformed JSON, ErrInvalidPaymentEvent) to a dead-letter topic
// so we never silently drop events.
type KafkaConsumer struct {
	reader *kafka.Reader
	dlq    *kafka.Writer
	log    *zap.SugaredLogger
}

// NewKafkaConsumer creates a new Kafka consumer with an attached DLQ writer.
func NewKafkaConsumer(cfg *config.KafkaConfig, log *zap.SugaredLogger) *KafkaConsumer {
	log = log.With("component", "kafka_consumer")

	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{cfg.Brokers},
		GroupID:     cfg.PaymentGroupID,
		Topic:       cfg.OrderCreatedTopic,
		StartOffset: kafka.FirstOffset,
	})

	dlq := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers),
		Topic:                  paymentDLQTopic,
		Balancer:               &kafka.LeastBytes{},
		AllowAutoTopicCreation: true,
		WriteTimeout:           cfg.WriteTimeout,
	}

	return &KafkaConsumer{
		reader: r,
		dlq:    dlq,
		log:    log,
	}
}

// Start consumes messages and invokes the handler. Unprocessable messages
// are written to the DLQ topic and their offsets committed so they do not
// block the partition.
func (c *KafkaConsumer) Start(ctx context.Context, handler domain.PaymentService) error {
	c.log.Info("Starting Kafka consumer for topic: order.created")

	for {
		c.log.Debug("Polling Kafka...")
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
			c.log.Errorw("Failed to unmarshal event — dead-lettering",
				"error", err, "payload", string(msg.Value), "offset", msg.Offset)
			c.deadLetter(ctx, msg, "invalid_payload", err)
			c.commitOrLog(ctx, msg)
			continue
		}

		if err := handler.ProcessOrder(ctx, &event); err != nil {
			// Business-invalid input (e.g. OrderID<=0, Amount<0) is
			// permanently unprocessable. Dead-letter and commit.
			if errors.Is(err, domain.ErrInvalidPaymentEvent) {
				c.log.Warnw("Invalid payment event — dead-lettering",
					"error", err, "order_id", event.OrderID, "offset", msg.Offset)
				c.deadLetter(ctx, msg, "invalid_event", err)
				c.commitOrLog(ctx, msg)
				continue
			}

			// Any other error is treated as transient: do NOT commit so
			// the message will be re-delivered by Kafka's group rebalance.
			c.log.Errorw("Failed to process order — will retry",
				"order_id", event.OrderID, "error", err, "offset", msg.Offset)
			continue
		}

		c.commitOrLog(ctx, msg)
	}
}

// deadLetter writes the original message (with metadata headers) to the
// DLQ topic and increments the observability counter ONLY on successful
// write. Failures here are logged — at that point the best we can do
// is keep processing the consumer loop and rely on Kafka's consumer-lag
// metric + the dlq_messages_total gap to alert operators that messages
// are being lost.
//
// Correctness note: the counter is incremented AFTER a successful
// WriteMessages, not before. An earlier version bumped the counter
// unconditionally at function entry, which meant that a transient
// Kafka leader-election error (observed during cluster warm-up) would
// leave the counter reporting "N messages dead-lettered" while those
// N messages were actually dropped on the floor. Metric vs. reality
// must match — otherwise alerts on dlq_messages_total become useless.
func (c *KafkaConsumer) deadLetter(ctx context.Context, msg kafka.Message, reason string, cause error) {
	dlqMsg := kafka.Message{
		Key:   msg.Key,
		Value: msg.Value,
		Headers: []kafka.Header{
			{Key: "x-original-topic", Value: []byte(msg.Topic)},
			{Key: "x-original-partition", Value: []byte(strconv.Itoa(msg.Partition))},
			{Key: "x-original-offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
			{Key: "x-dlq-reason", Value: []byte(reason)},
			{Key: "x-dlq-error", Value: []byte(cause.Error())},
		},
	}
	if err := c.dlq.WriteMessages(ctx, dlqMsg); err != nil {
		c.log.Errorw("Failed to write to DLQ topic — message lost",
			"error", err, "topic", paymentDLQTopic, "reason", reason,
			"original_offset", msg.Offset)
		return
	}
	observability.DLQMessagesTotal.WithLabelValues(paymentDLQTopic, reason).Inc()
}

// commitOrLog commits a message offset and logs (but does not return)
// commit errors — the consumer loop is designed to tolerate commit
// failures because Kafka will re-deliver on next rebalance.
func (c *KafkaConsumer) commitOrLog(ctx context.Context, msg kafka.Message) {
	if err := c.reader.CommitMessages(ctx, msg); err != nil {
		c.log.Errorw("Failed to commit offset", "error", err, "offset", msg.Offset)
	}
}

// Close closes the consumer and DLQ writer.
func (c *KafkaConsumer) Close() error {
	rErr := c.reader.Close()
	dErr := c.dlq.Close()
	if rErr != nil {
		return rErr
	}
	return dErr
}
