package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"

	"booking_monitor/internal/application/booking"
)

// Stage5IntakeTopic is the Kafka topic Stage 5 publishes booking
// reservations to. The `.v5` suffix avoids name collisions with any
// future stage that picks a different intake design.
const Stage5IntakeTopic = "booking.intake.v5"

// IntakePublisher publishes Stage 5 booking reservation messages to
// Kafka with synchronous acks=all semantics. The publish call blocks
// until the broker has confirmed write to all in-sync replicas (or
// the context deadline fires).
//
// Why acks=all (RequireAll): this is the durability layer that
// replaces ephemeral Redis Stream. acks=1 (leader-only) is faster
// but loses messages on a leader-only ack + leader crash before
// replication. The whole point of Stage 5 is to eliminate the
// ghost-202 window, so cheaping out at the durability gate would
// defeat the purpose.
//
// Why a separate publisher (not reusing kafkaPublisher): the
// existing kafkaPublisher accepts a domain.OutboxEvent and routes
// by EventType. Stage 5 intake is not an OutboxEvent — it's a
// hot-path booking reservation with a flat key/value contract. Two
// separate writers keeps the contracts crisp and lets each tune its
// own client settings (batch size, linger, etc).
//
// Implements booking.IntakePublisher (defined in
// internal/application/booking/intake.go) so the BookingService can
// hold the interface and tests can mock it without importing kafka.
type IntakePublisher struct {
	writer       *kafka.Writer
	writeTimeout time.Duration
}

// Compile-time assertion that *IntakePublisher satisfies
// booking.IntakePublisher.
var _ booking.IntakePublisher = (*IntakePublisher)(nil)

// NewIntakePublisher constructs the Stage 5 intake publisher.
// AllowAutoTopicCreation is enabled so the topic appears on first
// use without manual broker setup — matches kafkaPublisher's
// behaviour and the Stage 5 README expectations.
func NewIntakePublisher(cfg MessagingConfig) *IntakePublisher {
	timeout := cfg.WriteTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	w := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Topic:                  Stage5IntakeTopic,
		Balancer:               &kafka.Hash{}, // partition by message key (order_id)
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
		WriteTimeout:           timeout,
		// Async=false (default). Stage 5's contract is "ack received
		// before the handler returns 202", so the Publish call MUST
		// block. Async would defeat the durability guarantee.
		BatchTimeout: 1 * time.Millisecond, // flush almost immediately
		BatchSize:    1,                    // one message per write
	}
	return &IntakePublisher{
		writer:       w,
		writeTimeout: timeout,
	}
}

// ErrPublishTimeout signals the Kafka broker did not ack within the
// configured WriteTimeout. The caller (Stage 5 BookingService) MUST
// treat this as a publish failure and call RevertInventory on the
// already-deducted Redis qty.
var ErrPublishTimeout = errors.New("kafka intake publish timed out before ack")

// PublishIntake writes the booking reservation to Kafka and blocks
// until the broker confirms acks=all (or the ctx / writeTimeout
// fires, whichever sooner). The message key is the order_id string
// so all messages for the same order land on the same partition,
// preserving per-order ordering for any downstream consumer that
// cares.
func (p *IntakePublisher) PublishIntake(ctx context.Context, msg booking.IntakeMessage) error {
	if msg.OrderID == uuid.Nil {
		return fmt.Errorf("intake publisher: refusing to publish with zero order_id")
	}

	payload, err := encodeIntakeMessage(msg)
	if err != nil {
		return fmt.Errorf("intake publisher: encode message: %w", err)
	}

	// Bound the publish by the configured WriteTimeout in addition to
	// the caller's ctx. The kafka.Writer respects WriteTimeout for
	// network I/O but not the broker ack roundtrip — the explicit
	// ctx-with-timeout is the belt to kafka.Writer.WriteTimeout's
	// suspenders.
	publishCtx, cancel := context.WithTimeout(ctx, p.writeTimeout)
	defer cancel()

	err = p.writer.WriteMessages(publishCtx, kafka.Message{
		Key:   []byte(msg.OrderID.String()),
		Value: payload,
		Time:  time.Now().UTC(),
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w (after %s)", ErrPublishTimeout, p.writeTimeout)
		}
		return fmt.Errorf("intake publisher: write: %w", err)
	}
	return nil
}

// Close flushes and shuts down the writer. Mirrors kafkaPublisher.Close
// with the same 10s bounded-close guard so OnStop can never hang on a
// slow broker.
func (p *IntakePublisher) Close() error {
	done := make(chan error, 1)
	go func() { done <- p.writer.Close() }()

	select {
	case err := <-done:
		return err
	case <-time.After(kafkaCloseTimeout):
		return fmt.Errorf("IntakePublisher.Close: timed out after %s", kafkaCloseTimeout)
	}
}
