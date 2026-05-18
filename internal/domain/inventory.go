package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrTicketTypeRuntimeMetadataMissing = errors.New("ticket_type runtime metadata missing")

// DeductInventoryResult is the outcome of the Redis-side load-shed gate.
//
// Accepted=false with err=nil means "sold out" — no stream message was
// produced and the caller should surface domain.ErrSoldOut.
//
// Accepted=true carries the runtime snapshot that the booking hot path
// needs to construct a domain.Order without a synchronous Postgres
// lookup.
type DeductInventoryResult struct {
	Accepted    bool
	EventID     uuid.UUID
	AmountCents int64
	Currency    string
}

// InventoryRepository defines the interface for hot inventory management.
// This is typically implemented by a fast in-memory store like Redis.
//
//go:generate mockgen -destination=../mocks/mock_inventory_repository.go -package=mocks . InventoryRepository
type InventoryRepository interface {
	// SetTicketTypeRuntime seeds BOTH Redis runtime keys for a ticket type:
	//
	//   - `ticket_type_meta:{id}`  immutable booking snapshot fields
	//   - `ticket_type_qty:{id}`   live inventory counter
	//
	// Used by CreateEvent's success path: a freshly-created ticket_type
	// must be immediately bookable without waiting for a background
	// rehydrate.
	SetTicketTypeRuntime(ctx context.Context, ticketType TicketType) error

	// SetTicketTypeMetadata writes ONLY the immutable booking metadata key
	// (`ticket_type_meta:{id}`) for a ticket type.
	//
	// Used by the booking hot path's cold-fill repair when qty exists but
	// metadata is missing: the repair MUST NOT overwrite the live qty
	// counter because Redis may legitimately be ahead of Postgres by the
	// in-flight-booking delta.
	SetTicketTypeMetadata(ctx context.Context, ticketType TicketType) error

	// DeleteTicketTypeRuntime removes both runtime keys for a ticket type.
	//
	// Best-effort cleanup for rollback / compensation paths. Callers may
	// ignore transient Redis failures if the primary source-of-truth action
	// (e.g. DB row delete) already succeeded.
	DeleteTicketTypeRuntime(ctx context.Context, ticketTypeID uuid.UUID) error

	// DeductInventory atomically decrements the inventory count and,
	// on success, publishes the booking onto orders:stream with the
	// caller-provided orderID + reservedUntil and the runtime metadata
	// looked up from `ticket_type_meta:{id}`.
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
	// ticketTypeID is the customer-facing identifier. Redis resolves the
	// rest of the booking snapshot (`event_id`, `amount_cents`,
	// `currency`) from `ticket_type_meta:{id}` inside the same Lua call,
	// so the hot path avoids the synchronous Postgres `GetByID` lookup
	// that PR #90 cached.
	//
	// Sold-out returns `DeductInventoryResult{Accepted:false}, nil`.
	// `ErrTicketTypeRuntimeMetadataMissing` means qty existed but the
	// metadata key was absent; the Lua script restored the decrement
	// before returning, so the caller may cold-fill metadata and retry
	// once without leaking inventory or double-enqueueing a stream
	// message.
	DeductInventory(
		ctx context.Context,
		orderID uuid.UUID,
		ticketTypeID uuid.UUID,
		userID int,
		count int,
		reservedUntil time.Time,
	) (DeductInventoryResult, error)

	// RevertInventory restores inventory count.
	// compensationID is used for idempotency (e.g. order:{id} or stream msg_id)
	RevertInventory(ctx context.Context, ticketTypeID uuid.UUID, count int, compensationID string) error

	// GetInventory returns the cached inventory count for a ticket type,
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
	GetInventory(ctx context.Context, ticketTypeID uuid.UUID) (qty int, found bool, err error)
}

// Stage5InventoryRepository extends InventoryRepository with the
// Stage 5 Lua variant that omits the XADD step.
//
// Stage 5 (Damai-aligned durable intake) replaces ephemeral Redis
// Stream with replicated Kafka as the handler→worker queue. The
// inventory atomicity (DECRBY + condition check + amount calc) still
// runs in Lua, but the Lua script does NOT publish to orders:stream.
// Instead the application layer reads the result, constructs the
// Kafka message, and publishes with acks=all. On Kafka publish
// failure the application calls RevertInventory to re-add the
// inventory.
//
// Stages 2-4 keep using InventoryRepository.DeductInventory (which
// performs DECRBY + XADD atomically). Stage 5 binary injects the
// extended interface and calls DeductInventoryNoStream instead. The
// concrete `redisInventoryRepository` implements both interfaces; the
// stage binary chooses which view to inject.
//
// See `docs/d12/README.md` for the per-stage architecture matrix.
type Stage5InventoryRepository interface {
	InventoryRepository

	// DeductInventoryNoStream is the Stage 5 variant. Identical to
	// DeductInventory's atomicity semantics — DECRBY guarded by
	// availability + HMGET booking metadata + amount_cents
	// computation — but skips the XADD orders:stream step.
	//
	// Returns the same DeductInventoryResult. The caller (Stage 5
	// BookingService) is responsible for constructing the Kafka
	// message and publishing it with acks=all, calling
	// InventoryRepository.RevertInventory on publish failure.
	DeductInventoryNoStream(
		ctx context.Context,
		ticketTypeID uuid.UUID,
		count int,
	) (DeductInventoryResult, error)
}
