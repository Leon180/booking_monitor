package application

import (
	"context"

	"booking_monitor/internal/domain"
)

// EventPublisher is the application-layer port for "publish an
// outbox event to the external message bus." Lives in `application`
// because the only consumer is `OutboxRelay` (an application
// service); the implementation
// (`internal/infrastructure/messaging.NewKafkaPublisher`) is the
// boundary adapter.
//
// Previously the interface had a generic `Publish(topic, payload)`
// signature. PR-D12.4 changed it to `PublishOutboxEvent(ctx, e)`
// because the saga-compensation latency histogram needs the
// outbox event's CreatedAt timestamp threaded onto
// `kafka.Message.Time` — the histogram's start point. Without
// this thread-through, `kafka.Message.Time` defaults to the Go
// zero value and `time.Since(msg.Time)` returns ~63 BILLION
// seconds, breaking the histogram silently. See
// `domain/event.go` OutboxEvent doc + the PR-D12.4
// integration test for the full data-path contract.
//
//go:generate mockgen -source=messaging.go -destination=../mocks/messaging_mock.go -package=mocks
type EventPublisher interface {
	// PublishOutboxEvent publishes the given outbox event to the
	// message bus. The implementation MUST set the underlying
	// transport's wall-clock-timestamp field (e.g.
	// `kafka.Message.Time`) to `e.CreatedAt()` so consumers can
	// measure end-to-end latency from event-creation to
	// processing. The topic is derived from `e.EventType()`.
	PublishOutboxEvent(ctx context.Context, e domain.OutboxEvent) error
	Close() error
}
