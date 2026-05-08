//go:build integration

package pgintegration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"booking_monitor/internal/application/booking"
	bookingsync "booking_monitor/internal/application/booking/sync"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for D12 Stage 1's synchronous booking service
// (booking/sync) against a real postgres:15-alpine container with
// all migrations applied.
//
// Stage 1's whole architectural point is the SQL: BEGIN; SELECT FOR
// UPDATE; UPDATE event_ticket_types; INSERT orders; COMMIT. None of
// it is meaningfully testable by mocking the DB — these tests pin
// the contract that:
//
//   - Inventory is correctly decremented on success (not over-
//     decremented under contention).
//   - Sold-out rolls back without committing the order.
//   - Concurrent bookings on the same ticket_type serialize
//     correctly via the row lock + over-sell prevention.
//   - Duplicate-active-order returns domain.ErrUserAlreadyBought
//     (NOT a wrapped SQL error) AND does not consume inventory —
//     this is the Codex round-1 P2 fix from sync.Service slice 1.
//   - Ticket-type-not-found returns domain.ErrTicketTypeNotFound
//     before any DB write fires.
//
// All five tests run their own container (StartPostgres in the
// shared-harness helper); ~3-4s per test. No state leakage between
// tests.

// ────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────

// stage1Harness wires StartPostgres + a sync.Service ready for
// BookTicket. Returns the harness (for direct DB inspection in
// assertions) and the service.
func stage1Harness(t *testing.T) (*pgintegration.Harness, *bookingsync.Service) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	cfg := &config.Config{
		Booking: config.BookingConfig{ReservationWindow: 15 * time.Minute},
	}
	orderRepo := postgres.NewPostgresOrderRepository(h.DB)
	svc := bookingsync.NewService(h.DB, orderRepo, cfg)
	return h, svc
}

// seedTicketType inserts an event + a ticket_type row directly via
// SQL, bypassing both the event repo and ticket_type repo. Returns
// (eventID, ticketTypeID). Inventory is set to availableTickets;
// price_cents = 2000 ($20), currency = "usd".
func seedTicketType(t *testing.T, h *pgintegration.Harness, availableTickets int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	eventID := uuid.New()
	h.SeedEvent(t, eventID.String(), "Stage1 Test Event", availableTickets)

	ttID, err := uuid.NewV7()
	require.NoError(t, err)
	const stmt = `
		INSERT INTO event_ticket_types (
			id, event_id, name, price_cents, currency,
			total_tickets, available_tickets, version
		) VALUES ($1::uuid, $2::uuid, 'GA', 2000, 'usd', $3, $3, 0)`
	_, err = h.DB.Exec(stmt, ttID.String(), eventID.String(), availableTickets)
	require.NoError(t, err, "seed event_ticket_types")
	return eventID, ttID
}

// availableTicketsNow returns the current available_tickets count
// for a ticket_type — used in conservation-check assertions.
func availableTicketsNow(t *testing.T, h *pgintegration.Harness, ttID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, h.DB.QueryRow(
		"SELECT available_tickets FROM event_ticket_types WHERE id = $1::uuid",
		ttID.String(),
	).Scan(&n))
	return n
}

// orderCount returns the current orders row count — for verifying
// rollback didn't leave a partial row.
func orderCount(t *testing.T, h *pgintegration.Harness, ttID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, h.DB.QueryRow(
		"SELECT COUNT(*) FROM orders WHERE ticket_type_id = $1::uuid",
		ttID.String(),
	).Scan(&n))
	return n
}

// ────────────────────────────────────────────────────────────────
// Tests
// ────────────────────────────────────────────────────────────────

// TestSyncBooking_HappyPath — single booking decrements inventory
// + persists the order row with status='awaiting_payment' (Stage 4
// contract parity, NOT 'confirmed'), reserved_until in the future,
// and the price snapshot frozen onto the row.
func TestSyncBooking_HappyPath(t *testing.T) {
	h, svc := stage1Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketType(t, h, 10)
	require.Equal(t, 10, availableTicketsNow(t, h, ttID))

	order, err := svc.BookTicket(ctx, 42, ttID, 1)
	require.NoError(t, err)

	// Inventory decremented by exactly 1.
	assert.Equal(t, 9, availableTicketsNow(t, h, ttID),
		"available_tickets should decrement by quantity on success")

	// Exactly one order row.
	assert.Equal(t, 1, orderCount(t, h, ttID))

	// Order status matches Stage 4 contract.
	assert.Equal(t, domain.OrderStatusAwaitingPayment, order.Status())
	assert.Equal(t, 42, order.UserID())
	assert.Equal(t, ttID, order.TicketTypeID())
	assert.Equal(t, 1, order.Quantity())
	assert.Equal(t, int64(2000), order.AmountCents(),
		"amount_cents = price_cents * quantity")
	assert.Equal(t, "usd", order.Currency())
	assert.True(t, order.ReservedUntil().After(time.Now()),
		"reserved_until should be in the future")
}

// TestSyncBooking_SoldOut — when available_tickets < quantity, the
// transaction rolls back: no order row created, inventory unchanged.
// Returns domain.ErrSoldOut (NOT a wrapped SQL error).
func TestSyncBooking_SoldOut(t *testing.T) {
	h, svc := stage1Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketType(t, h, 1)

	// Consume the single ticket.
	_, err := svc.BookTicket(ctx, 1, ttID, 1)
	require.NoError(t, err)
	require.Equal(t, 0, availableTicketsNow(t, h, ttID))
	require.Equal(t, 1, orderCount(t, h, ttID))

	// Second booking should fail with ErrSoldOut.
	_, err = svc.BookTicket(ctx, 2, ttID, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrSoldOut),
		"sold-out should return domain.ErrSoldOut sentinel; got %v", err)

	// Inventory + order count unchanged.
	assert.Equal(t, 0, availableTicketsNow(t, h, ttID))
	assert.Equal(t, 1, orderCount(t, h, ttID),
		"sold-out rollback must NOT leave a partial order row")
}

// TestSyncBooking_TicketTypeNotFound — ticket_type id doesn't exist
// → returns domain.ErrTicketTypeNotFound before any DB write fires.
func TestSyncBooking_TicketTypeNotFound(t *testing.T) {
	h, svc := stage1Harness(t)
	ctx := context.Background()

	missingID, err := uuid.NewV7()
	require.NoError(t, err)

	_, err = svc.BookTicket(ctx, 1, missingID, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTicketTypeNotFound),
		"missing ticket_type should return domain.ErrTicketTypeNotFound; got %v", err)

	// No orders rows anywhere (different ttID; just sanity-check global state).
	var n int
	require.NoError(t, h.DB.QueryRow("SELECT COUNT(*) FROM orders").Scan(&n))
	assert.Equal(t, 0, n)
}

// TestSyncBooking_ConcurrentContention — N goroutines book the
// same ticket_type with available_tickets=K (K < N). The row lock
// serializes them; exactly K succeed, exactly (N-K) get ErrSoldOut,
// inventory ends at 0, exactly K orders persist.
//
// This is the architectural cost the comparison harness will
// surface against Stages 2-4: stage 1 throughput collapses under
// contention because every booking serializes on the same row lock.
func TestSyncBooking_ConcurrentContention(t *testing.T) {
	h, svc := stage1Harness(t)
	ctx := context.Background()

	const (
		stock     = 5
		attempts  = 20
		quantity  = 1
	)
	_, ttID := seedTicketType(t, h, stock)

	var (
		wg          sync.WaitGroup
		successes   int
		soldOutErrs int
		otherErrs   int
		mu          sync.Mutex
	)

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(uid int) {
			defer wg.Done()
			_, err := svc.BookTicket(ctx, uid, ttID, quantity)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, domain.ErrSoldOut):
				soldOutErrs++
			default:
				otherErrs++
				t.Logf("unexpected concurrent error: %v", err)
			}
		}(i + 1) // user_id starts at 1 to keep them all distinct
	}
	wg.Wait()

	assert.Equal(t, stock, successes,
		"exactly stock-many bookings should succeed under contention")
	assert.Equal(t, attempts-stock, soldOutErrs,
		"the rest should fail with ErrSoldOut")
	assert.Equal(t, 0, otherErrs,
		"no booking should fail with anything other than nil or ErrSoldOut")
	assert.Equal(t, 0, availableTicketsNow(t, h, ttID),
		"all stock consumed")
	assert.Equal(t, stock, orderCount(t, h, ttID),
		"exactly stock-many order rows persisted")
}

// TestSyncBooking_DuplicateActiveOrder — the same user + same event
// with an active (non-failed/non-compensated) order trips the
// uq_orders_user_event partial unique index (migration 000011).
// The Stage 4 contract maps this to domain.ErrUserAlreadyBought →
// HTTP 409. Stage 1's direct INSERT path mirrors that mapping
// (Codex round-1 P2 fix).
//
// CRITICAL: the rejected second booking MUST NOT consume inventory.
// The first booking decremented from 5 → 4; the second booking
// SELECT FOR UPDATEs the row, sees 4 available, decrements to 3,
// then trips the constraint on INSERT — at which point the WHOLE
// tx must roll back, restoring inventory to 4 (NOT leaving it at
// 3). This test pins that rollback behavior explicitly per Codex's
// review guidance.
func TestSyncBooking_DuplicateActiveOrder(t *testing.T) {
	h, svc := stage1Harness(t)
	ctx := context.Background()

	_, ttID := seedTicketType(t, h, 5)
	require.Equal(t, 5, availableTicketsNow(t, h, ttID))

	// First booking: succeeds.
	_, err := svc.BookTicket(ctx, 99, ttID, 1)
	require.NoError(t, err)
	assert.Equal(t, 4, availableTicketsNow(t, h, ttID))
	assert.Equal(t, 1, orderCount(t, h, ttID))

	// Second booking by SAME user against SAME event (via SAME
	// ticket_type) should trip uq_orders_user_event.
	_, err = svc.BookTicket(ctx, 99, ttID, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrUserAlreadyBought),
		"duplicate-active-order MUST return domain.ErrUserAlreadyBought (NOT a wrapped SQL error). "+
			"Without the 23505 sentinel mapping, the HTTP handler returns 500 instead of 409, breaking Stage 4 contract parity. Got: %v", err)

	// CRITICAL: tx rollback restored inventory to 4, did NOT leave
	// it at 3 (the value seen mid-tx after the doomed UPDATE).
	assert.Equal(t, 4, availableTicketsNow(t, h, ttID),
		"rejected duplicate booking must NOT consume inventory — tx rollback restores available_tickets")

	// Order count unchanged at 1.
	assert.Equal(t, 1, orderCount(t, h, ttID),
		"rejected duplicate booking must NOT leave a partial order row")
}

// Compile-time assertion: bookingsync.NewService returns a value
// assignable to booking.Service. If the interface drifts, the test
// build fails here, before any container is started.
var _ booking.Service = (*bookingsync.Service)(nil)
