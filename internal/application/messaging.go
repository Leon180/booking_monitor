package application

import "context"

// EventPublisher is the application-layer port for "publish a payload
// to an external message bus." Lives in `application` because the
// only consumer is `OutboxRelay` (an application service); the
// implementation (`internal/infrastructure/messaging.NewKafkaPublisher`)
// is the boundary adapter.
//
// Previously lived in `internal/domain/`. Moved here in CP2.5 because
// it has no domain-rule semantics — it's a pure infrastructure-port
// shape with a generic `Publish(topic, payload)` signature any
// pub/sub broker can satisfy. The wire-contract type names (event
// types like `EventTypeOrderCreated` + the `OrderEvent` payload
// shape) correctly stay in `domain` — only the *transport* port moves.
//
//go:generate mockgen -source=messaging.go -destination=../mocks/messaging_mock.go -package=mocks
type EventPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	Close() error
}
