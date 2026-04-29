package domain

import (
	"context"

	"github.com/google/uuid"
)

// InventoryRepository defines the interface for hot inventory management.
// This is typically implemented by a fast in-memory store like Redis.
//
//go:generate mockgen -destination=../mocks/mock_inventory_repository.go -package=mocks . InventoryRepository
type InventoryRepository interface {
	// SetInventory sets the initial inventory count for an event.
	SetInventory(ctx context.Context, eventID uuid.UUID, count int) error

	// DeductInventory atomically decrements the inventory count and,
	// on success, publishes the booking onto orders:stream with the
	// caller-provided orderID embedded in the payload.
	//
	// orderID flows end-to-end (handler → Lua → stream → worker
	// `domain.NewOrder` → DB) so the id the client receives at HTTP
	// 202 is the same id that lands in DB after async processing —
	// and the same id that PEL retries reuse, instead of generating a
	// fresh uuid per redelivery.
	//
	// userID is also passed through to the stream message; duplicate-
	// purchase prevention happens at the DB UNIQUE(user_id, event_id)
	// constraint downstream.
	//
	// Returns true if successful, false if insufficient inventory
	// (ErrSoldOut). On false, NO stream message is produced — the
	// orderID is silently discarded so the client gets a sold_out
	// response with no order intent persisted.
	DeductInventory(ctx context.Context, orderID uuid.UUID, eventID uuid.UUID, userID int, count int) (bool, error)

	// RevertInventory restores inventory count.
	// compensationID is used for idempotency (e.g. order:{id} or stream msg_id)
	RevertInventory(ctx context.Context, eventID uuid.UUID, count int, compensationID string) error
}
