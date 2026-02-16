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
	// Returns true if successful, false if insufficient inventory (ErrSoldOut).
	DeductInventory(ctx context.Context, eventID int, userID int, count int) (bool, error)
}
