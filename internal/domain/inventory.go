package domain

import (
	"context"

	"github.com/google/uuid"
)

// InventoryRepository defines the interface for hot inventory management.
// This is typically implemented by a fast in-memory store like Redis.
//
// B3 sharding note: `INVENTORY_SHARDS` (config) splits an event's
// inventory across N independent Redis Lua keys to relieve the
// single-key bottleneck characterised in
// docs/benchmarks/20260502_132247_vu_scaling/. With N=1 (B3.1
// default), there is only one shard and behavior is byte-identical
// to pre-B3 single-key Redis. With N>1 (post B3.2), DeductInventory
// random-picks a shard and retries on alternates if a shard is
// depleted; the chosen shard is returned so saga compensation can
// revert to the same shard.
//
//go:generate mockgen -destination=../mocks/mock_inventory_repository.go -package=mocks . InventoryRepository
type InventoryRepository interface {
	// SetInventory sets the initial inventory count for an event,
	// distributing the count across the configured number of shards
	// (even split with remainder going to shard 0).
	SetInventory(ctx context.Context, eventID uuid.UUID, count int) error

	// DeductInventory atomically decrements inventory on a single
	// shard and, on success, publishes the booking onto orders:stream
	// with the caller-provided orderID + the chosen shard embedded
	// in the payload. With N>1 shards, the implementation retries on
	// alternate shards if the first-picked shard is depleted (a
	// per-shard depletion does NOT mean the event is sold out).
	//
	// orderID flows end-to-end (handler → Lua → stream → worker
	// `domain.NewOrder` → DB) so the id the client receives at HTTP
	// 202 is the same id that lands in DB after async processing.
	//
	// Returns:
	//   - success bool — true if any shard accepted the booking
	//   - shard int     — which shard accepted the booking; 0 when
	//                     the event is sold out (no shard accepted).
	//                     Saga compensation MUST revert to this shard
	//                     to keep per-shard accounting honest.
	//   - err error     — Redis / Lua failure; orthogonal to sold-out
	//
	// On sold-out, no stream message is produced and shard is 0.
	DeductInventory(ctx context.Context, orderID uuid.UUID, eventID uuid.UUID, userID int, count int) (success bool, shard int, err error)

	// RevertInventory restores inventory count to the specified shard.
	// Caller (saga compensator) MUST pass the shard the original deduct
	// landed in (carried via OrderCreatedEvent → OrderFailedEvent in B3.2;
	// hardcoded to 0 in B3.1 since N=1 has only one shard).
	//
	// compensationID is used for idempotency (e.g. order:{id} or stream msg_id).
	RevertInventory(ctx context.Context, eventID uuid.UUID, shard int, count int, compensationID string) error
}
