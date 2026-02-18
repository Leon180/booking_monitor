package domain

import "context"

// EventPublisher publishes domain events to an external message bus (e.g. Kafka).
//
//go:generate mockgen -source=messaging.go -destination=../mocks/messaging_mock.go -package=mocks
type EventPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
	Close() error
}
