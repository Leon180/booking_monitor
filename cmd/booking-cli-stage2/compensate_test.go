//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgintegration "booking_monitor/test/integration/postgres"
)

// Integration test for compensateDanglingEvent — Stage 2's
// /events handler calls this when SetTicketTypeRuntime fails
// post-PG-commit. The function MUST delete both rows in FK-safe
// order (ticket_type first, then event); a regression that
// reversed the order would trip 23503 foreign_key_violation and
// leave the dangling rows in place — the very condition the
// compensation exists to clear.
//
// Test lives in package main alongside the function under test
// because the cmd binary's package is `main` and not import-able
// from cross-package tests. Build-tagged `integration` so plain
// `go test ./...` doesn't pull a Postgres container.

func TestStage2_CompensateDanglingEvent_DeletesBothRows(t *testing.T) {
	ctx := context.Background()
	pgH := pgintegration.StartPostgres(ctx, t)

	eventID := uuid.New()
	ttID, err := uuid.NewV7()
	require.NoError(t, err)

	// Seed the same shape handleCreateEvent inserts so the
	// compensation tests against a realistic post-commit state.
	_, err = pgH.DB.ExecContext(ctx,
		`INSERT INTO events (id, name, total_tickets, available_tickets, version)
		 VALUES ($1::uuid, 'Compensation Test', 10, 10, 0)`,
		eventID.String())
	require.NoError(t, err, "seed events row")
	_, err = pgH.DB.ExecContext(ctx,
		`INSERT INTO event_ticket_types (
			id, event_id, name, price_cents, currency,
			total_tickets, available_tickets, version
		) VALUES ($1::uuid, $2::uuid, 'GA', 2000, 'usd', 10, 10, 0)`,
		ttID.String(), eventID.String())
	require.NoError(t, err, "seed event_ticket_types row")

	require.NoError(t, compensateDanglingEvent(ctx, pgH.DB, eventID, ttID),
		"compensation must succeed against fully-seeded rows")

	var n int
	require.NoError(t, pgH.DB.QueryRow(
		"SELECT COUNT(*) FROM events WHERE id = $1::uuid", eventID.String(),
	).Scan(&n))
	assert.Equal(t, 0, n, "events row deleted")
	require.NoError(t, pgH.DB.QueryRow(
		"SELECT COUNT(*) FROM event_ticket_types WHERE id = $1::uuid", ttID.String(),
	).Scan(&n))
	assert.Equal(t, 0, n, "event_ticket_types row deleted")
}

func TestStage2_CompensateDanglingEvent_IdempotentOnMissingRows(t *testing.T) {
	ctx := context.Background()
	pgH := pgintegration.StartPostgres(ctx, t)

	// Both rows DON'T exist — exercises the "partial earlier
	// compensation, retry" claim from the function's doc comment.
	missingEventID := uuid.New()
	missingTTID, err := uuid.NewV7()
	require.NoError(t, err)

	require.NoError(t, compensateDanglingEvent(ctx, pgH.DB, missingEventID, missingTTID),
		"DELETE on missing rows must be a no-op, not an error — required for retry safety")
}
