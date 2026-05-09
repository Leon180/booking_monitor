package messaging

import (
	"context"
	"fmt"
	"time"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"

	"github.com/segmentio/kafka-go"
)

// kafkaCloseTimeout bounds how long kafka.Writer.Close() is allowed to
// block flushing in-flight batches during shutdown. The underlying
// kafka-go Writer does not accept a ctx on Close(), so we enforce the
// timeout with a done-channel race. Without this bound, fx OnStop
// could hang indefinitely on a broker that's slow to ack the final
// flush.
const kafkaCloseTimeout = 10 * time.Second

type kafkaPublisher struct {
	writer *kafka.Writer
}

// NewKafkaPublisher creates a new Kafka-backed EventPublisher.
func NewKafkaPublisher(cfg MessagingConfig) application.EventPublisher {
	timeout := cfg.WriteTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	w := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Balancer:               &kafka.LeastBytes{},
		AllowAutoTopicCreation: true,
		WriteTimeout:           timeout,
	}
	return &kafkaPublisher{writer: w}
}

// PublishOutboxEvent writes the outbox event to Kafka with
// `kafka.Message.Time` set to the event's CreatedAt timestamp.
// This is load-bearing for the saga-compensation latency histogram
// (PR-D12.4) — the consumer reads `msg.Time` and computes
// `time.Since(msg.Time)` against it. Pre-D12.4 this method took
// `(topic, payload)` and left `kafka.Message.Time` at the Go zero
// value, so the histogram's `time.Since` call returned ~63 BILLION
// seconds (epoch-since-year-1) and every observation landed in the
// `+Inf` bucket. See `internal/application/messaging.go`'s
// EventPublisher doc + `internal/domain/event.go`'s OutboxEvent
// doc for the full data-path contract.
//
// Defense-in-depth zero-CreatedAt guard: the production path
// (factory → repo.Create → ListPending → relay → here) always
// supplies a non-zero CreatedAt. But a hypothetical future path
// that constructs an OutboxEvent via `domain.OutboxEvent{}` zero
// literal or a `ReconstructOutboxEvent` call with `time.Time{}`
// would silently corrupt every histogram observation downstream.
// Refusing to publish at this boundary contains the blast radius
// to a single failed publish (which the relay logs + retries on
// next tick) instead of permanent histogram corruption.
func (p *kafkaPublisher) PublishOutboxEvent(ctx context.Context, e domain.OutboxEvent) error {
	if e.CreatedAt().IsZero() {
		return fmt.Errorf("kafkaPublisher: refusing to publish outbox event %s with zero CreatedAt — would corrupt saga compensation latency histogram", e.ID())
	}
	return p.writer.WriteMessages(ctx, messageForOutboxEvent(e))
}

// messageForOutboxEvent is the pure function that maps a domain
// outbox event to a kafka.Message. Extracted so a unit test can
// pin the data-path contract (Time = e.CreatedAt(), Topic =
// e.EventType(), Value = e.Payload()) without needing a real
// kafka.Writer or a testcontainers Kafka broker. Broker-side
// preservation of Time is verified by the live HTTP smoke in
// Slice 6 + D12.5's harness; that's an end-to-end concern that
// depends on Kafka topic configuration (CreateTime semantics).
func messageForOutboxEvent(e domain.OutboxEvent) kafka.Message {
	return kafka.Message{
		Topic: e.EventType(),
		Value: e.Payload(),
		Time:  e.CreatedAt(),
	}
}

// Close flushes the writer but gives up after kafkaCloseTimeout so the
// shutdown path can never block forever on a slow broker.
//
// Note: if the timeout fires, the background goroutine calling
// p.writer.Close() is leaked — kafka-go's Writer.Close() has no
// cancellation hook. This is a single-goroutine, shutdown-only leak
// that cannot be fixed without upstream support (or switching to a
// writer that accepts context on Close). Acceptable trade-off: the
// process is terminating anyway.
func (p *kafkaPublisher) Close() error {
	done := make(chan error, 1)
	go func() { done <- p.writer.Close() }()

	select {
	case err := <-done:
		return err
	case <-time.After(kafkaCloseTimeout):
		return fmt.Errorf("kafkaPublisher.Close: timed out after %s", kafkaCloseTimeout)
	}
}
