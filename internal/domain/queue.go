package domain

import (
	"context"

	"github.com/google/uuid"
)

// OrderMessage represents the payload in the Redis stream consumed
// by the worker. The ID is a Redis-stream entry id (format
// "millis-seq"), distinct from the domain Order.ID — it identifies
// the queue message, not the eventual order.
type OrderMessage struct {
	ID         string    // Redis stream message ID (NOT a UUID)
	UserID     int       // External user reference (this service does not own users)
	EventID    uuid.UUID // FK to events.id
	Quantity   int
	RetryCount int
}

// OrderQueue defines abstract queue operations
//
//go:generate mockgen -source=queue.go -destination=../mocks/queue_mock.go -package=mocks
type OrderQueue interface {
	// EnsureGroup creates the consumer group if not exists
	EnsureGroup(ctx context.Context) error

	// Subscribe starts consuming messages processing them with handler.
	// It blocks until context is cancelled using XREADGROUP.
	Subscribe(ctx context.Context, handler func(ctx context.Context, msg *OrderMessage) error) error
}
