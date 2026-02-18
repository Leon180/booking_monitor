package domain

import "context"

// OrderMessage represents the payload in the stream
type OrderMessage struct {
	ID         string // Stream Message ID
	UserID     int
	EventID    int
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
