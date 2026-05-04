//go:build integration

package pgintegration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for postgresTicketTypeRepository against a real
// postgres:15-alpine container with all migrations applied.
//
// What these tests pin that the row-mapper unit tests
// (`ticket_type_row.go` is structurally tested only via uow_test.go's
// schema round-trip) cannot:
//
//   - Nullable column round-trip semantics: sale_starts_at /
//     sale_ends_at / per_user_limit / area_label all map to optional
//     domain types (*time.Time / *int / "" sentinel). A bug in
//     `toDomain` that swapped Valid handling would only surface here.
//   - The `uq_ticket_type_name_per_event` UNIQUE constraint — postgres
//     fires `23505`, the repo translates to ErrTicketTypeNameTaken.
//     A future drop-of-Code-check would silently swallow the typed
//     error.
//   - `ListByEventID` ordering by id ASC (UUIDv7 → insertion order).
//   - `GetByID` ErrTicketTypeNotFound mapping for sql.ErrNoRows.
//
// Each top-level Test* function gets its own container; sub-tests
// share via Reset.

func ticketTypeRepoHarness(t *testing.T) (*pgintegration.Harness, domain.TicketTypeRepository) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewPostgresTicketTypeRepository(h.DB)
	return h, repo
}

// newTicketType builds a valid domain.TicketType for fixture use. All
// optional fields default to nil / "" so the row-mapper exercises the
// NULL-write path; tests that need non-null optionals override via the
// pointer args.
func newTicketType(t *testing.T, eventID uuid.UUID, name string) domain.TicketType {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	tt, err := domain.NewTicketType(
		id, eventID, name, 2000, "usd", 100,
		nil, nil, nil, "",
	)
	require.NoError(t, err)
	return tt
}

func TestTicketTypeRepository_CreateAndGetByID_AllNullableFieldsAbsent(t *testing.T) {
	h, repo := ticketTypeRepoHarness(t)
	ctx := context.Background()

	eventID := seedEventForOrder(t, h)
	tt := newTicketType(t, eventID, "VIP early-bird")

	created, err := repo.Create(ctx, tt)
	require.NoError(t, err)
	assert.Equal(t, tt.ID(), created.ID())

	got, err := repo.GetByID(ctx, tt.ID())
	require.NoError(t, err)

	assert.Equal(t, tt.ID(), got.ID())
	assert.Equal(t, eventID, got.EventID())
	assert.Equal(t, "VIP early-bird", got.Name())
	assert.Equal(t, int64(2000), got.PriceCents())
	assert.Equal(t, "usd", got.Currency())
	assert.Equal(t, 100, got.TotalTickets())
	assert.Equal(t, 100, got.AvailableTickets())
	// All optional fields write as NULL → read back as nil / "":
	assert.Nil(t, got.SaleStartsAt(), "nil sale_starts_at must round-trip as nil after DB NULL")
	assert.Nil(t, got.SaleEndsAt())
	assert.Nil(t, got.PerUserLimit())
	assert.Equal(t, "", got.AreaLabel(), `empty area_label must round-trip as "" after DB NULL`)
}

func TestTicketTypeRepository_CreateAndGetByID_AllNullableFieldsSet(t *testing.T) {
	h, repo := ticketTypeRepoHarness(t)
	ctx := context.Background()

	eventID := seedEventForOrder(t, h)
	saleStarts := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Microsecond)
	saleEnds := saleStarts.Add(24 * time.Hour)
	limit := 5
	id, err := uuid.NewV7()
	require.NoError(t, err)
	tt, err := domain.NewTicketType(
		id, eventID, "VIP A 區", 5000, "twd", 50,
		&saleStarts, &saleEnds, &limit, "VIP A",
	)
	require.NoError(t, err)

	_, err = repo.Create(ctx, tt)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, id)
	require.NoError(t, err)

	require.NotNil(t, got.SaleStartsAt(), "set sale_starts_at must round-trip as non-nil")
	assert.True(t, got.SaleStartsAt().Equal(saleStarts), "sale_starts_at must round-trip with µs precision")
	require.NotNil(t, got.SaleEndsAt())
	assert.True(t, got.SaleEndsAt().Equal(saleEnds))
	require.NotNil(t, got.PerUserLimit())
	assert.Equal(t, 5, *got.PerUserLimit())
	assert.Equal(t, "VIP A", got.AreaLabel())
	// Currency is normalised to lowercase before the factory writes it.
	assert.Equal(t, "twd", got.Currency())
}

func TestTicketTypeRepository_GetByID_NotFound(t *testing.T) {
	_, repo := ticketTypeRepoHarness(t)
	ctx := context.Background()

	missing, err := uuid.NewV7()
	require.NoError(t, err)

	_, err = repo.GetByID(ctx, missing)
	assert.ErrorIs(t, err, domain.ErrTicketTypeNotFound,
		"missing row must surface as the typed sentinel — string-matching the postgres error is an anti-pattern")
}

func TestTicketTypeRepository_Create_DuplicateNameForSameEvent_MapsTo_ErrTicketTypeNameTaken(t *testing.T) {
	h, repo := ticketTypeRepoHarness(t)
	ctx := context.Background()

	eventID := seedEventForOrder(t, h)

	first := newTicketType(t, eventID, "VIP")
	_, err := repo.Create(ctx, first)
	require.NoError(t, err)

	// Second insert with same (event_id, name) must hit
	// uq_ticket_type_name_per_event → 23505 → ErrTicketTypeNameTaken.
	second := newTicketType(t, eventID, "VIP")
	_, err = repo.Create(ctx, second)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrTicketTypeNameTaken),
		"23505 violation of uq_ticket_type_name_per_event must map to ErrTicketTypeNameTaken — caller branches on this for HTTP 409 mapping")
}

func TestTicketTypeRepository_Create_SameNameAcrossDifferentEvents_OK(t *testing.T) {
	h, repo := ticketTypeRepoHarness(t)
	ctx := context.Background()

	eventA := seedEventForOrder(t, h)
	eventB := seedEventForOrder(t, h)

	a := newTicketType(t, eventA, "Standard")
	b := newTicketType(t, eventB, "Standard")

	_, err := repo.Create(ctx, a)
	require.NoError(t, err)
	_, err = repo.Create(ctx, b)
	assert.NoError(t, err, "uniqueness is per-event, not global — 'Standard' on different events must coexist")
}

func TestTicketTypeRepository_ListByEventID_OrderByIDAsc(t *testing.T) {
	h, repo := ticketTypeRepoHarness(t)
	ctx := context.Background()

	eventID := seedEventForOrder(t, h)

	// Three ticket types created in sequence — UUIDv7 IDs are time-
	// prefixed so id ASC is creation-time ASC. Pin the ordering.
	a := newTicketType(t, eventID, "First")
	_, err := repo.Create(ctx, a)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)
	b := newTicketType(t, eventID, "Second")
	_, err = repo.Create(ctx, b)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)
	c := newTicketType(t, eventID, "Third")
	_, err = repo.Create(ctx, c)
	require.NoError(t, err)

	got, err := repo.ListByEventID(ctx, eventID)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, a.ID(), got[0].ID(), "first inserted must come first under id ASC")
	assert.Equal(t, b.ID(), got[1].ID())
	assert.Equal(t, c.ID(), got[2].ID())
}

func TestTicketTypeRepository_ListByEventID_NoRowsReturnsNilNotError(t *testing.T) {
	_, repo := ticketTypeRepoHarness(t)
	ctx := context.Background()

	// Event id that was never inserted. Pre-D4.1-cutover semantic:
	// ListByEventID does NOT verify event existence — empty result is
	// nil, NOT ErrEventNotFound. Documented in the repo doc-comment;
	// pinned here so a future "tighten to ErrEventNotFound" change
	// can't silently break this contract.
	missingEvent, err := uuid.NewV7()
	require.NoError(t, err)

	got, err := repo.ListByEventID(ctx, missingEvent)
	require.NoError(t, err, "no-rows must NOT surface as an error — events with no ticket types are valid pre-D4.1 state")
	assert.Nil(t, got)
}
