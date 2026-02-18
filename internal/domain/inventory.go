package domain

import "context"

// InventoryRepository defines the interface for hot inventory management.
// This is typically implemented by a fast in-memory store like Redis.
//
//go:generate mockgen -destination=../mocks/mock_inventory_repository.go -package=mocks . InventoryRepository
type InventoryRepository interface {
	// SetInventory sets the initial inventory count for an event.
	SetInventory(ctx context.Context, eventID int, count int) error

	// DeductInventory atomically decrements the inventory count.
	// userID is passed through to the Redis stream for async order processing.
	// Duplicate purchase prevention is handled by the database UNIQUE constraint.
	// Returns true if successful, false if insufficient inventory (ErrSoldOut).
	DeductInventory(ctx context.Context, eventID int, userID int, count int) (bool, error)

	// RevertInventory restores inventory count.
	// Used for compensation in DLQ scenarios.
	RevertInventory(ctx context.Context, eventID int, count int) error
}
