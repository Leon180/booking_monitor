package domain

import (
	"context"
	"time"

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
	// caller-provided orderID + reservedUntil embedded in the payload.
	//
	// orderID flows end-to-end (handler → Lua → stream → worker
	// `domain.NewReservation` → DB) so the id the client receives at
	// HTTP 202 is the same id that lands in DB after async processing
	// — and the same id that PEL retries reuse, instead of generating
	// a fresh uuid per redelivery.
	//
	// userID is also passed through to the stream message; duplicate-
	// purchase prevention happens at the DB UNIQUE(user_id, event_id)
	// constraint downstream.
	//
	// reservedUntil is the Pattern A reservation TTL — computed by
	// BookingService as `time.Now().Add(window)`. Threaded through Lua
	// + stream as a unix-seconds integer (the smallest serialisation
	// that survives Lua's number-as-double representation without
	// precision loss for any time within ±5e9 seconds of the epoch),
	// then re-parsed back to time.Time by the worker. The timestamp
	// is the application's "your reservation is valid until X"
	// commitment; the D6 expiry sweeper later compares
	// `WHERE reserved_until < NOW()` against this column.
	//
	// Returns true if successful, false if insufficient inventory
	// (ErrSoldOut). On false, NO stream message is produced — the
	// orderID is silently discarded so the client gets a sold_out
	// response with no order intent persisted.
	DeductInventory(ctx context.Context, orderID uuid.UUID, eventID uuid.UUID, userID int, count int, reservedUntil time.Time) (bool, error)

	// RevertInventory restores inventory count.
	// compensationID is used for idempotency (e.g. order:{id} or stream msg_id)
	RevertInventory(ctx context.Context, eventID uuid.UUID, count int, compensationID string) error

	// GetInventory returns the cached inventory count for an event,
	// and a `found` bool that distinguishes "key absent in Redis"
	// (`found=false`) from "key present with value 0" (`found=true,
	// qty=0` — the legitimate sold-out steady state).
	//
	// The bool is load-bearing for the inventory-drift detector
	// (PR-D / `recon` subcommand): a fresh / post-FLUSHALL Redis
	// returns `(0, false, nil)` and produces a `cache_missing` alert
	// (operator runs rehydrate); a key that decremented all the way
	// to zero returns `(0, true, nil)` and is treated like any other
	// `cache_low_excess` case (the worker may be stuck) — collapsing
	// the two would mis-label the alert and route the operator to
	// the wrong branch of the runbook.
	//
	// Returns the wrapped Redis error on transient infra failure
	// (caller handles via metric bump + retry next sweep). The
	// `(qty, found)` pair is only meaningful when `err == nil`.
	GetInventory(ctx context.Context, eventID uuid.UUID) (qty int, found bool, err error)
}
