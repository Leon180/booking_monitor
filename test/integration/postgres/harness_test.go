//go:build integration

package pgintegration_test

import (
	"context"
	"testing"

	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHarness_Boot verifies the harness:
//   1. Spins up a postgres testcontainer
//   2. Applies every migration cleanly
//   3. Leaves the schema in the post-000011 shape (events, orders,
//      events_outbox, order_status_history all present)
//   4. The widened partial index from migration 000011 exists
//
// Smoke-test for the harness itself — if this fails, every other
// integration test in this package will fail too. Keeping it isolated
// makes diagnosis trivial: a failure here means harness layout drift
// (migrations dir moved, schema renamed, etc.), NOT a repository bug.
func TestHarness_Boot(t *testing.T) {
	ctx := context.Background()
	h := pgintegration.StartPostgres(ctx, t)

	// Verify all four core tables exist after migrations.
	wantTables := []string{"events", "orders", "events_outbox", "order_status_history"}
	for _, name := range wantTables {
		t.Run("table_"+name, func(t *testing.T) {
			var exists bool
			err := h.DB.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.tables
					WHERE table_schema = 'public' AND table_name = $1
				)`, name).Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists, "table %q must exist after migrations", name)
		})
	}

	// Verify the post-000011 partial index — its presence is a
	// load-bearing query-plan invariant for FindStuckCharging /
	// FindStuckFailed sweeps.
	t.Run("partial_index_post_000011", func(t *testing.T) {
		var indexDef string
		err := h.DB.QueryRowContext(ctx, `
			SELECT indexdef FROM pg_indexes
			WHERE schemaname = 'public'
			  AND tablename = 'orders'
			  AND indexname = 'idx_orders_status_updated_at_partial'`).Scan(&indexDef)
		require.NoError(t, err, "partial index must exist after migration 000011")
		assert.Contains(t, indexDef, "charging")
		assert.Contains(t, indexDef, "pending")
		assert.Contains(t, indexDef, "failed")
	})

	// Tripwire for the lib/pq-multi-statement assumption (see
	// applyMigrations comment in harness.go). Migration 000009 has
	// THREE statements: CREATE TABLE order_status_history + two
	// CREATE INDEX statements. If a future driver switch silently
	// applied only the first, the table would exist but the
	// indexes wouldn't. Verifying both indexes from 000009
	// catches that class of regression.
	wantIndexes := []struct {
		table  string
		index  string
		source string
	}{
		// Migration 000007 has 1 statement (CREATE INDEX CONCURRENTLY);
		// kept here for symmetric coverage of the partial outbox
		// index. CONCURRENTLY would fail if applyMigrations wrapped
		// each file in BEGIN/COMMIT (the migrate CLI's default) —
		// db.ExecContext does NOT open a transaction so this works.
		{"events_outbox", "events_outbox_pending_idx", "000007"},
		// Migration 000009 — the multi-statement-per-file canary.
		// File contains CREATE TABLE + 2 CREATE INDEX statements.
		// Verifying both indexes catches a regression where the
		// multi-statement protocol breaks and only the first
		// statement applies.
		{"order_status_history", "idx_order_status_history_order_id_occurred", "000009"},
		{"order_status_history", "idx_order_status_history_occurred", "000009"},
	}
	for _, wi := range wantIndexes {
		t.Run("index_post_"+wi.source+"_"+wi.index, func(t *testing.T) {
			var exists bool
			err := h.DB.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM pg_indexes
					WHERE schemaname = 'public'
					  AND tablename = $1 AND indexname = $2
				)`, wi.table, wi.index).Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists,
				"index %q on %q from migration %s must exist — its absence indicates lib/pq's simple-protocol multi-statement support broke",
				wi.index, wi.table, wi.source)
		})
	}
}

// TestHarness_Reset verifies that Reset clears row data without
// dropping the schema. After Reset, tables must be present (count
// query succeeds) and empty (count == 0).
func TestHarness_Reset(t *testing.T) {
	ctx := context.Background()
	h := pgintegration.StartPostgres(ctx, t)

	// Seed an event so Reset has something to clear.
	h.SeedEvent(t, "11111111-1111-1111-1111-111111111111", "Probe Event", 100)

	var beforeCount int
	require.NoError(t,
		h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&beforeCount))
	require.Equal(t, 1, beforeCount, "seed must succeed before Reset")

	h.Reset(t)

	var afterCount int
	require.NoError(t,
		h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&afterCount))
	assert.Equal(t, 0, afterCount, "Reset must clear events rows")
}
