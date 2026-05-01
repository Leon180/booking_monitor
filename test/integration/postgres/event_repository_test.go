//go:build integration

package pgintegration_test

import (
	"context"
	"testing"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for postgresEventRepository against a real
// postgres:15-alpine container with all migrations applied.
//
// Load-bearing contracts pinned (none reachable via row-mapper unit
// tests):
//
//   - DecrementTicket's `WHERE id = $1 AND available_tickets >= $2`
//     predicate — the sold-out detection mechanism. RowsAffected==0
//     maps to domain.ErrSoldOut. A regression that dropped the
//     `>= $2` clause would silently allow oversell.
//
//   - IncrementTicket's `WHERE ... AND available_tickets + $2 <=
//     total_tickets` predicate — the "no over-restore" guard for
//     saga compensation. Without it, a duplicate compensation event
//     could push available_tickets above total_tickets.
//
//   - GetByIDForUpdate row-lock acquisition. Verified by the UoW
//     suite; here we just confirm it returns the row identical to
//     GetByID under the no-contention path.
//
//   - ErrEventNotFound on missing-row, ErrSoldOut on Decrement of
//     a depleted event — both load-bearing for callers that
//     errors.Is on these sentinels.

func eventRepoHarness(t *testing.T) (*pgintegration.Harness, domain.EventRepository) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewPostgresEventRepository(h.DB)
	return h, repo
}

func newDomainEvent(t *testing.T, totalTickets int) domain.Event {
	t.Helper()
	ev, err := domain.NewEvent("Concert", totalTickets)
	require.NoError(t, err)
	return ev
}

// TestEventRepository_CreateAndGetByID: round-trip an event via Create
// + GetByID. Verifies all fields rehydrate correctly.
func TestEventRepository_CreateAndGetByID(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 100)

	created, err := repo.Create(ctx, ev)
	require.NoError(t, err)
	assert.Equal(t, ev.ID(), created.ID(),
		"Create must return the same id passed in (caller-generated)")

	got, err := repo.GetByID(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, ev.ID(), got.ID())
	assert.Equal(t, ev.Name(), got.Name())
	assert.Equal(t, ev.TotalTickets(), got.TotalTickets())
	assert.Equal(t, ev.AvailableTickets(), got.AvailableTickets())
}

// TestEventRepository_GetByID_NotFound: missing id returns
// domain.ErrEventNotFound, not a generic error. Pins the contract
// callers (event service, mapError) rely on.
//
// No h.Reset(t) — the harness gives each top-level test a fresh
// container so the schema is empty on entry. This is the canonical
// "no fixture needed" case.
func TestEventRepository_GetByID_NotFound(t *testing.T) {
	_, repo := eventRepoHarness(t)

	_, err := repo.GetByID(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrEventNotFound)
}

// TestEventRepository_GetByIDForUpdate_ReturnsSameAsGet: under no
// contention, GetByIDForUpdate returns the same row shape as GetByID.
// The lock-acquisition behavior is exercised by the UoW suite.
func TestEventRepository_GetByIDForUpdate_ReturnsSameAsGet(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 50)
	_, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	got, err := repo.GetByIDForUpdate(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, ev.ID(), got.ID())
	assert.Equal(t, ev.TotalTickets(), got.TotalTickets())
}

// TestEventRepository_DecrementTicket_HappyPath: available_tickets
// decreases by quantity when there's enough capacity.
func TestEventRepository_DecrementTicket_HappyPath(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 100)
	_, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	require.NoError(t, repo.DecrementTicket(ctx, ev.ID(), 25))

	got, err := repo.GetByID(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, 75, got.AvailableTickets(),
		"DecrementTicket must reduce available_tickets by the requested quantity")
	assert.Equal(t, 100, got.TotalTickets(),
		"total_tickets must remain unchanged")
}

// TestEventRepository_DecrementTicket_SoldOut: when requested quantity
// exceeds available_tickets, the WHERE predicate excludes the row →
// 0 rowsAffected → ErrSoldOut. Pins the oversell-prevention contract.
func TestEventRepository_DecrementTicket_SoldOut(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 5)
	_, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	// Request more than available → must surface as ErrSoldOut.
	err = repo.DecrementTicket(ctx, ev.ID(), 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrSoldOut,
		"Decrement requesting more than available must surface as ErrSoldOut, not a generic error")

	// Available_tickets must NOT have been mutated.
	got, err := repo.GetByID(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, 5, got.AvailableTickets(),
		"failed Decrement must not have partially mutated available_tickets")
}

// TestEventRepository_DecrementTicket_ExactlyAtLimit: requesting
// exactly the available quantity must succeed (boundary check on the
// `>=` predicate).
func TestEventRepository_DecrementTicket_ExactlyAtLimit(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 10)
	_, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	// Request exactly the available count → must succeed.
	require.NoError(t, repo.DecrementTicket(ctx, ev.ID(), 10))

	got, err := repo.GetByID(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, 0, got.AvailableTickets(),
		"Decrement of exactly available must zero the count")

	// Subsequent Decrement of any quantity must now fail.
	err = repo.DecrementTicket(ctx, ev.ID(), 1)
	assert.ErrorIs(t, err, domain.ErrSoldOut)
}

// TestEventRepository_IncrementTicket_HappyPath: available_tickets
// increases by quantity, capped by total_tickets via the WHERE
// predicate.
func TestEventRepository_IncrementTicket_HappyPath(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 100)
	_, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	// Decrement first so there's room to increment back up.
	require.NoError(t, repo.DecrementTicket(ctx, ev.ID(), 30))
	require.NoError(t, repo.IncrementTicket(ctx, ev.ID(), 30))

	got, err := repo.GetByID(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, 100, got.AvailableTickets(),
		"saga compensation: Increment back to original count")
}

// TestEventRepository_IncrementTicket_OverRestoreBlocked: incrementing
// past total_tickets is blocked by the `+ $2 <= total_tickets`
// predicate. Pins the saga-compensation safety property — a
// duplicate compensation event must not over-restore inventory.
//
// This is load-bearing: revert.lua is idempotent on the Redis side,
// but the DB-side IncrementTicket relies entirely on this WHERE clause
// for the same protection. A regression that dropped the predicate
// would silently let duplicate compensations push available_tickets
// above total_tickets.
func TestEventRepository_IncrementTicket_OverRestoreBlocked(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 50)
	_, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	// Event starts with available=total=50. Increment by 1 attempts
	// to push to 51, exceeding total_tickets.
	err = repo.IncrementTicket(ctx, ev.ID(), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds total tickets",
		"over-restore must surface a clear 'exceeds total tickets' error, not a silent no-op")

	got, err := repo.GetByID(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, 50, got.AvailableTickets(),
		"failed Increment must not have partially mutated available_tickets")
}

// TestEventRepository_Update_OnlyAvailableTickets: Update is narrowly
// scoped — its current SQL is `UPDATE events SET available_tickets =
// $1 WHERE id = $2`. Name + total_tickets are NOT mutated, even if
// the input has different values. This test pins the contract so a
// future scope-broadening change has to update the test alongside.
//
// If admin-rename ever becomes a use case, Update's SQL will need to
// be widened — and this test fail-loud is the intended signal that
// the contract changed.
func TestEventRepository_Update_OnlyAvailableTickets(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 100)
	created, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	// Build an Update input with both available_tickets AND name
	// mutated. The repo should persist the count but NOT the name.
	updated := domain.ReconstructEvent(created.ID(), "Renamed Concert",
		created.TotalTickets(), 42, created.Version())
	require.NoError(t, repo.Update(ctx, updated))

	got, err := repo.GetByID(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, 42, got.AvailableTickets(),
		"Update must persist available_tickets (the only field its SQL mutates)")
	assert.Equal(t, "Concert", got.Name(),
		"Update must NOT mutate name — the SQL only sets available_tickets. If this fails, Update's scope was broadened; update the test deliberately.")
}

// TestEventRepository_Delete: Delete removes the row. Subsequent
// GetByID returns ErrEventNotFound. Used by the EventService's
// dangling-row compensation path (when SetInventory fails).
func TestEventRepository_Delete(t *testing.T) {
	h, repo := eventRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 100)
	_, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	require.NoError(t, repo.Delete(ctx, ev.ID()))

	_, err = repo.GetByID(ctx, ev.ID())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrEventNotFound,
		"after Delete, GetByID must surface ErrEventNotFound")
}

// TestEventRepository_Delete_Idempotent: deleting a non-existent
// event must not error. Compensation may fire twice; each call must
// be safe.
//
// No h.Reset(t) — fresh container, no fixture needed.
func TestEventRepository_Delete_Idempotent(t *testing.T) {
	_, repo := eventRepoHarness(t)

	// Delete a never-existed id → must not error (idempotent).
	require.NoError(t, repo.Delete(context.Background(), uuid.New()),
		"Delete of non-existent id must be idempotent (no error) — compensation safety")
}
