// Package sync implements booking.Service for D12 Stage 1 — the
// pure-synchronous baseline.
//
// Stage 1 architecture: API → Postgres `BEGIN; SELECT FOR UPDATE;
// UPDATE event_ticket_types; INSERT orders; COMMIT;`. No Redis, no
// Kafka, no async worker, no out-of-process saga. The order row is
// committed before /book returns, but the response shape stays the
// same as Stage 4 (202 Accepted with status="reserved" +
// reserved_until + links.pay) so the existing
// `scripts/k6_two_step_flow.js` runs unmodified across all four
// stages — that's the apples-to-apples test of the comparison
// contract.
//
// The compensation path (abandon TTL expiry, payment_failed) lives
// in cmd/booking-cli-stage1/server.go as an in-binary sweeper
// goroutine. Without Redis or Kafka, compensation is a single PG
// transaction: SELECT FOR UPDATE event_ticket_types + UPDATE
// available_tickets += qty + UPDATE orders SET status='compensated'.
// That goroutine doesn't live here because Service is a request-path
// abstraction; the sweeper runs independently.
//
// Inventory source-of-truth: `event_ticket_types.available_tickets`
// is the only inventory column Stage 1 reads or writes. The legacy
// `events.available_tickets` column was frozen post-D4.1 and MUST
// NOT be used here — using it would make Stage 1 fast but not
// comparable to Stages 2-4.
package sync

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

// pgUniqueViolation is the SQLSTATE code Postgres returns when a
// unique constraint is violated (uq_orders_user_event for our
// schema). The orderRepository's Create method maps this to
// domain.ErrUserAlreadyBought; the direct INSERT path here mirrors
// that mapping so the HTTP handler returns 409 (matching Stage 4)
// instead of 500.
const pgUniqueViolation = "23505"

// Service implements booking.Service using a single Postgres
// transaction with pessimistic SELECT FOR UPDATE. Bookings serialize
// on the row lock; this is the architectural baseline D12's
// comparison harness will measure against the async stages.
type Service struct {
	db                *sql.DB
	orderRepo         domain.OrderRepository
	reservationWindow time.Duration
}

// NewService wires the synchronous booking Service. db is used
// directly for the BookTicket transaction (the pattern doesn't fit
// the existing UnitOfWork abstraction, which is built around the
// async worker's INSERT semantics). orderRepo handles the read
// paths (GetOrder, GetBookingHistory) — those don't need
// synchronization, so they delegate to the same repo Stage 4 uses.
//
// Compile-time assertion that *Service implements booking.Service
// lives at the bottom of this file.
func NewService(db *sql.DB, orderRepo domain.OrderRepository, cfg *config.Config) *Service {
	return &Service{
		db:                db,
		orderRepo:         orderRepo,
		reservationWindow: cfg.Booking.ReservationWindow,
	}
}

// BookTicket runs the full sync booking transaction:
//
//  1. Mint orderID (UUIDv7).
//  2. BEGIN.
//  3. SELECT FOR UPDATE the ticket_type row (blocks concurrent
//     bookings on the same ticket_type until COMMIT — the
//     architectural cost the comparison surfaces).
//  4. If available_tickets < quantity → ROLLBACK +
//     domain.ErrSoldOut.
//  5. UPDATE event_ticket_types SET available_tickets -= quantity,
//     version += 1 WHERE id = $1.
//  6. INSERT orders (status='awaiting_payment',
//     reserved_until = NOW() + reservation_window,
//     amount_cents + currency snapshotted from the locked row).
//  7. COMMIT.
//
// Returns the constructed domain.Order. The caller (HTTP handler)
// shapes the 202 response with reserved_until + links.pay.
//
// Errors mirror Stage 4:
//   - domain.ErrTicketTypeNotFound — ticket_type id doesn't exist
//   - domain.ErrSoldOut             — available_tickets < quantity
//   - domain.ErrInvalid*            — invariant violation surfaced
//                                     via NewReservation
func (s *Service) BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, quantity int) (domain.Order, error) {
	// Pre-BEGIN input validation. Defense-in-depth — the API
	// handler's binding tags already reject these, but failing fast
	// here avoids wasting a SELECT FOR UPDATE on a request that will
	// fail invariant validation in domain.NewReservation later.
	// Sentinel choice mirrors NewReservation so the HTTP handler's
	// existing mapError logic returns the same status codes.
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Order{}, fmt.Errorf("begin tx: %w", err)
	}
	// Roll back on any error path; if Commit succeeds Rollback is a
	// no-op per database/sql contract.
	defer func() { _ = tx.Rollback() }()

	// Step 3+4: SELECT FOR UPDATE the ticket_type row + read the
	// price snapshot fields in the same query. The lock is held until
	// COMMIT/ROLLBACK; concurrent bookings on the same ticket_type
	// serialize here — by design.
	var (
		eventID          uuid.UUID
		availableTickets int
		priceCents       int64
		currency         string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT event_id, available_tickets, price_cents, currency
		  FROM event_ticket_types
		 WHERE id = $1
		   FOR UPDATE
	`, ticketTypeID).Scan(&eventID, &availableTickets, &priceCents, &currency)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Order{}, domain.ErrTicketTypeNotFound
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("select for update ticket_type: %w", err)
	}
	if availableTickets < quantity {
		return domain.Order{}, domain.ErrSoldOut
	}

	// Step 5: decrement inventory. version is a defensive bump for
	// any future optimistic-concurrency consumers reading the row;
	// the row lock subsumes the write-side correctness here.
	if _, err = tx.ExecContext(ctx, `
		UPDATE event_ticket_types
		   SET available_tickets = available_tickets - $1,
		       version = version + 1
		 WHERE id = $2
	`, quantity, ticketTypeID); err != nil {
		return domain.Order{}, fmt.Errorf("decrement available_tickets: %w", err)
	}

	// Step 6: construct the domain.Order via the canonical factory.
	// reservedUntil = now() + window in UTC (matches Stage 4's row
	// timezone semantics; Postgres TIMESTAMPTZ stores UTC internally
	// regardless, but normalizing at the boundary keeps Go-side
	// comparisons unambiguous).
	reservedUntil := time.Now().Add(s.reservationWindow).UTC()
	amountCents := priceCents * int64(quantity)
	order, err := domain.NewReservation(orderID, userID, eventID, ticketTypeID, quantity, reservedUntil, amountCents, currency)
	if err != nil {
		return domain.Order{}, fmt.Errorf("construct reservation: %w", err)
	}

	// Step 7: persist the order row through a direct INSERT inside
	// our tx. The shared postgresOrderRepository.Create exists but
	// opens its own DB connection by default; threading our tx into
	// it would require either refactoring the repo's exec field
	// (touches every other consumer) or building a tx-bound wrapper.
	// For Stage 1's single-implementation context, the simplest path
	// is the direct INSERT here. Critically, this MUST mirror the
	// repository's 23505 → ErrUserAlreadyBought mapping (uq_orders_
	// user_event partial unique index from migration 000011) so the
	// HTTP handler returns the same 409 the rest of the system does
	// — without this, Stage 1 would return 500 on duplicate-active-
	// order, breaking Stage-4-contract parity (Codex round-2 P2).
	if _, err = tx.ExecContext(ctx, `
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
			return domain.Order{}, domain.ErrUserAlreadyBought
		}
		return domain.Order{}, fmt.Errorf("insert order: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return domain.Order{}, fmt.Errorf("commit booking tx: %w", err)
	}
	return order, nil
}

// GetOrder delegates to the shared OrderRepository — the read path
// doesn't need synchronization. Returns the same
// domain.ErrOrderNotFound shape as Stage 4 so the handler's 404
// mapping is unchanged.
func (s *Service) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return s.orderRepo.GetByID(ctx, id)
}

// GetBookingHistory delegates to the shared OrderRepository's
// ListOrders. Pagination + status filter same as Stage 4.
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
// the interface ever drifts, the build fails here, not at the cmd
// wiring site.
var _ booking.Service = (*Service)(nil)
