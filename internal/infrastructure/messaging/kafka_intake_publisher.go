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
func NewIntakePublisher(cfg MessagingConfig) booking.IntakePublisher {
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

	// PR #128 A10 (review fixup): keep a bounded context but root it at
	// Background, NOT the caller's ctx. The pre-A10 shape used
	// `context.WithTimeout(ctx, p.writeTimeout)` which is fine but
	// inherits cancellation from the gin request ctx — if the client
	// disconnects mid-publish, the publish is cancelled before broker
	// ack, leaving Redis inventory deducted with no Kafka durability
	// guarantee. Stage 5's whole point is at-least-once Kafka durability
	// on the booking hot path, so we want the publish to complete (or
	// time out on its own clock) regardless of client disconnect.
	//
	// Why not just pass `ctx` directly: the audit's claim that
	// `kafka.Writer.WriteTimeout` ALONE bounds the gin handler was
	// incorrect. Per kafka-go's WriteMessages select loop, only the
	// caller's ctx Done channel arms the early-cancel path; the
	// Writer's internal Background-rooted produce ctx only unblocks
	// `batch.done` *after* WriteTimeout elapses, but the gin goroutine
	// is still pinned for that full duration. More importantly, the
	// ErrPublishTimeout sentinel detection (`errors.Is(err,
	// context.DeadlineExceeded)`) only fires when the *caller's* ctx
	// carries the deadline — kafka-go wraps the internal timeout into
	// a network/protocol error that does NOT unwrap to
	// context.DeadlineExceeded. Restoring an explicit deadline keeps
	// both the bounded wall-clock AND the typed-sentinel behaviour.
	//
	// The alloc cost (~80B *timerCtx + timer pair per publish ≈
	// 640 KB/s + 8k timer create/cancel pairs/s at the 8k publish/s
	// intake ceiling) is real but the wrong trade-off vs the
	// durability + observability regression A10's first form
	// introduced. Pooling the timer is the proper optimisation if
	// the alloc surface ever shows up as material on the hot path.
	publishCtx, cancel := context.WithTimeout(context.Background(), p.writeTimeout)
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
