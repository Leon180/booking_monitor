// Package synclua implements booking.Service for D12 Stage 2 — the
// Redis-Lua-atomic-deduct + sync-Postgres-INSERT variant.
//
// Stage 2 architecture: API → `EVAL deduct_sync.lua` (atomic Redis
// DECRBY + metadata read returning the price snapshot) → INSERT into
// `orders` in a single Postgres statement → 202 Accepted. On INSERT
// failure (uq_orders_user_event 23505 OR transient PG error) the
// service runs revert.lua against the same `ticket_type_qty:{id}`
// key to undo the Redis deduct, gated by the existing
// `saga:reverted:order:<id>` SETNX guard so the revert is idempotent
// across this path AND any later saga compensation that fires on
// the same orderID.
//
// Inventory source-of-truth: `ticket_type_qty:{id}` in Redis. Stage
// 2 does NOT update `event_ticket_types.available_tickets` on the
// booking hot path — and the compensator deliberately doesn't
// touch it either (see `cmd/booking-cli-stage2/server.go` →
// stage2Compensator). The forward path and the compensation path
// are symmetric: PG admin / drift-detector readers see the seeded
// `total_tickets` value as a stable snapshot, and Redis qty is the
// only authoritative live count. An asymmetric compensator that
// incremented PG would inflate the column +qty on every abandon,
// drifting upward forever (Codex round-1 P1). The architectural
// cost Stage 2 surfaces vs Stage 1 is the SoT migration, not the
// loss of PG inventory rows.
//
// Saturation point by design: Stage 2's bottleneck is the synchronous
// PG INSERT, not Redis Lua. The comparison harness (PR-D12.5) frames
// Stages 3-4 as the "headroom under contention that Stage 2 physically
// can't reach because the sync DB write serializes." This package is
// the architectural pivot in that comparison.
package synclua

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
)

// pgUniqueViolation mirrors `booking/sync/service.go` — Postgres
// SQLSTATE for a unique constraint violation. The orders schema
// has `uq_orders_user_event` (partial unique on (user_id, event_id)
// WHERE status IN active states); a 23505 here means "this user
// already has an active order on this event". The repo-managed
// `Create` path has the same mapping; the direct INSERT here
// reproduces it so the HTTP handler returns 409 like Stage 4 does
// instead of 500.
const pgUniqueViolation = "23505"

// Deducter is the small interface synclua.Service requires from the
// Redis-side hot path. It is satisfied by
// `cache.RedisSyncDeducter` in production and a hand-rolled fake in
// unit tests, sidestepping miniredis's known Lua-fidelity gaps.
//
// Defined here per the "accept interfaces, return structs" rule in
// `.claude/rules/golang/coding-style.md` — the interface lives at
// the consumption site so the implementer (cache pkg) is free to
// expose more methods than this package needs.
type Deducter interface {
	Deduct(ctx context.Context, ticketTypeID uuid.UUID, quantity int) (domain.DeductInventoryResult, error)
}

// Service implements booking.Service for Stage 2. Constructed once
// per process and reused for every request; method receivers are
// pointer-typed so the embedded *redis.Script in the deducter is
// not copied per call.
type Service struct {
	db                *sql.DB
	deducter          Deducter
	orderRepo         domain.OrderRepository
	ticketTypeRepo    domain.TicketTypeRepository
	inventoryRepo     domain.InventoryRepository
	reservationWindow time.Duration
}

// NewService wires the Stage 2 booking service. inventoryRepo is
// retained for two helper paths only:
//
//   - SetTicketTypeMetadata — cold-fill repair when Lua returns
//     `metadata_missing` (Redis qty exists but ticket_type_meta:{id}
//     was evicted / never seeded).
//   - RevertInventory — INSERT-failure-revert + the saga compensator's
//     forward-compatible idempotency key (`order:<id>`).
//
// db is held directly because the Stage 2 INSERT does not need the
// UnitOfWork machinery (single statement, no outbox).
//
// Compile-time assertion that *Service satisfies booking.Service
// lives at the bottom of this file.
func NewService(
	db *sql.DB,
	deducter Deducter,
	orderRepo domain.OrderRepository,
	ticketTypeRepo domain.TicketTypeRepository,
	inventoryRepo domain.InventoryRepository,
	cfg *config.Config,
) *Service {
	return &Service{
		db:                db,
		deducter:          deducter,
		orderRepo:         orderRepo,
		ticketTypeRepo:    ticketTypeRepo,
		inventoryRepo:     inventoryRepo,
		reservationWindow: cfg.Booking.ReservationWindow,
	}
}

// BookTicket runs the Stage 2 forward path:
//
//  1. Validate caller-controlled fields (defense-in-depth; the API
//     binding tags already reject these).
//  2. Mint orderID (UUIDv7) — flows handler response → Lua → INSERT.
//  3. Compute reservedUntil = now + reservation window (UTC).
//  4. Run deduct_sync.lua. If `metadata_missing`, repair from PG and
//     retry once.
//  5. On `sold_out` → ErrSoldOut. On `ok` → construct
//     domain.NewReservation with the Lua-supplied price snapshot.
//  6. INSERT orders. On unique-violation → ErrUserAlreadyBought
//     (and revert Redis). On any other PG error → wrap + revert
//     Redis. On NewReservation invariant failure → revert Redis.
//
// Errors:
//   - domain.ErrInvalidUserID / ErrInvalidOrderTicketTypeID /
//     ErrInvalidQuantity — caller-input invariants
//   - domain.ErrSoldOut — Redis qty exhausted
//   - domain.ErrUserAlreadyBought — uq_orders_user_event tripped
//   - domain.ErrTicketTypeRuntimeMetadataMissing — only if repair
//     also fails (signals operator-grade Redis/PG misalignment)
func (s *Service) BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, quantity int) (domain.Order, error) {
	if userID <= 0 {
		return domain.Order{}, domain.ErrInvalidUserID
	}
	if ticketTypeID == uuid.Nil {
		return domain.Order{}, domain.ErrInvalidOrderTicketTypeID
	}
	if quantity <= 0 {
		return domain.Order{}, domain.ErrInvalidQuantity
	}

	orderID, err := uuid.NewV7()
	if err != nil {
		return domain.Order{}, fmt.Errorf("mint order id: %w", err)
	}

	reservedUntil := time.Now().Add(s.reservationWindow).UTC()

	// Step 4: atomic Redis deduct. The Lua script has already
	// restored the qty if it's about to return metadata_missing OR
	// sold_out, so the only path that owes a revert is one where
	// Lua returned ok and a downstream step failed.
	result, err := s.deducter.Deduct(ctx, ticketTypeID, quantity)
	if errors.Is(err, domain.ErrTicketTypeRuntimeMetadataMissing) {
		if rerr := s.repairRuntimeMetadata(ctx, ticketTypeID); rerr != nil {
			return domain.Order{}, rerr
		}
		result, err = s.deducter.Deduct(ctx, ticketTypeID, quantity)
		if errors.Is(err, domain.ErrTicketTypeRuntimeMetadataMissing) {
			return domain.Order{}, fmt.Errorf("redis runtime metadata still missing after repair ticket_type_id=%s: %w", ticketTypeID, err)
		}
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("redis deduct ticket_type_id=%s: %w", ticketTypeID, err)
	}
	if !result.Accepted {
		return domain.Order{}, domain.ErrSoldOut
	}

	// Step 5: construct the domain.Order via the canonical factory.
	// Any factory invariant violation here means Lua succeeded
	// (qty is decremented in Redis) but the booking is invalid —
	// MUST revert before returning, otherwise we leak a ticket.
	order, err := domain.NewReservation(orderID, userID, result.EventID, ticketTypeID, quantity, reservedUntil, result.AmountCents, result.Currency)
	if err != nil {
		if revertErr := s.revertAfterFailure(ctx, ticketTypeID, quantity, orderID); revertErr != nil {
			// Revert failed: Redis qty is leaked. Wrap revertErr
			// (NOT the primary domain.ErrInvalid* sentinel) so the
			// error chain falls through to MapBookingError's 500
			// default — clients see "internal error", not the 400
			// the primary sentinel would map to. The original
			// invariant cause is in the message text for log
			// triage. (Codex round-3 P2.)
			return domain.Order{}, fmt.Errorf("redis revert failed after construct reservation (invariant: %v): %w", err, revertErr)
		}
		return domain.Order{}, fmt.Errorf("construct reservation: %w", err)
	}

	// Step 6: persist via direct INSERT. uq_orders_user_event
	// (migration 000011) is the partial unique index that catches
	// duplicate active orders; mirror Stage 1's mapping.
	if err := s.insertOrder(ctx, order); err != nil {
		if revertErr := s.revertAfterFailure(ctx, ticketTypeID, quantity, orderID); revertErr != nil {
			// Same logic as Step 5: when revert fails, the Redis
			// leak is the operationally-important error. Wrap
			// revertErr (NOT the primary err — domain.ErrUserAlreadyBought
			// for the 23505 case) so MapBookingError doesn't return
			// 409 on a leaked-inventory state. The 500 default leak-
			// guards against the client incorrectly believing a
			// duplicate-detection happened cleanly. (Codex round-3 P2.)
			return domain.Order{}, fmt.Errorf("redis revert failed after insert order (insert: %v): %w", err, revertErr)
		}
		return domain.Order{}, err
	}

	return order, nil
}

// insertOrder runs the single-statement INSERT that persists a
// Stage 2 reservation. The schema columns + ordering match Stage 1
// exactly so a future schema migration only needs to be auditioned
// in one place per stage.
func (s *Service) insertOrder(ctx context.Context, order domain.Order) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO orders (
			id, user_id, event_id, ticket_type_id, quantity, status,
			amount_cents, currency, reserved_until, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		order.ID(),
		order.UserID(),
		order.EventID(),
		order.TicketTypeID(),
		order.Quantity(),
		string(order.Status()),
		order.AmountCents(),
		order.Currency(),
		order.ReservedUntil(),
		order.CreatedAt(),
	); err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return domain.ErrUserAlreadyBought
		}
		return fmt.Errorf("insert order: %w", err)
	}
	return nil
}

// revertAfterFailure runs revert.lua against the deducted
// ticket_type qty when a downstream step (NewReservation invariant
// violation, INSERT failure) needs to undo the Redis decrement.
// The compensation ID matches the format the saga compensator
// would later use for this orderID — guaranteeing exactly-once
// semantics if both this path AND a saga compensation hypothetically
// fire on the same order (the SETNX guard inside revert.lua
// short-circuits the second caller).
//
// Callers chain the returned error through `fmt.Errorf` so the HTTP
// handler's standard error-mapping path surfaces both the primary
// failure AND the revert-failure-on-top in a single 500 response,
// rather than swallowing one or the other.
func (s *Service) revertAfterFailure(ctx context.Context, ticketTypeID uuid.UUID, quantity int, orderID uuid.UUID) error {
	return s.inventoryRepo.RevertInventory(ctx, ticketTypeID, quantity, "order:"+orderID.String())
}

// repairRuntimeMetadata fills `ticket_type_meta:{id}` from the
// authoritative ticket_type row in Postgres. Identical shape to
// Stage 4's `service.repairRuntimeMetadata` so the cold-fill
// behavior is uniform across the stages that share the
// metadata_missing return code.
//
// One difference: Stage 4 protects against thundering-herd by
// relying on the Lua script restoring qty before returning the
// special code (so concurrent N callers each repair-then-retry
// idempotently). Stage 2 inherits the same guarantee.
func (s *Service) repairRuntimeMetadata(ctx context.Context, ticketTypeID uuid.UUID) error {
	tt, err := s.ticketTypeRepo.GetByID(ctx, ticketTypeID)
	if err != nil {
		return fmt.Errorf("BookTicket lookup ticket_type_id=%s: %w", ticketTypeID, err)
	}
	if err := s.inventoryRepo.SetTicketTypeMetadata(ctx, tt); err != nil {
		return fmt.Errorf("BookTicket repair metadata ticket_type_id=%s: %w", ticketTypeID, err)
	}
	return nil
}

// GetOrder + GetBookingHistory delegate to the shared
// OrderRepository — both read paths are identical to Stage 1's so
// the only thing this package contributes vs Stage 1 is the
// Redis-fronted forward write path.
func (s *Service) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return s.orderRepo.GetByID(ctx, id)
}

func (s *Service) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize
	return s.orderRepo.ListOrders(ctx, pageSize, offset, status)
}

// Compile-time assertion: *Service satisfies booking.Service. If
// the interface ever grows a method, the build fails here, not at
// the cmd wiring site.
var _ booking.Service = (*Service)(nil)
