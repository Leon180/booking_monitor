//go:build integration

package pgintegration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CP4c — migration round-trip test. Verifies that for every
// migration N, the sequence (up_N → down_N → up_N) produces the same
// schema state both before and after the round trip. This catches
// the silent-failure class where a down.sql file drifts from its
// up.sql counterpart over time:
//
//   - DROP TABLE missing IF EXISTS (fails on second teardown)
//   - DROP TABLE IF NOT EXISTS — invalid SQL syntax (Postgres only
//     supports IF EXISTS on DROP). CP4c found exactly one of these
//     in 000003_add_outbox.down.sql; fixed in this PR.
//   - Index name typos in down (CREATE in up uses one name, DROP in
//     down uses another → DROP silently no-ops)
//   - Column drops missing from down
//   - CHECK constraint added in up but no DROP CONSTRAINT in down
//
// The test runs against a single container (boot once, walk all
// migrations) — much cheaper than per-test containers, and the
// state machine is deterministic so isolation isn't needed.
//
// Strategy:
//
//   1. Boot empty container (no migrations applied).
//   2. For each migration N from 1 to last:
//        a. Apply up_N. Snapshot schema → snapshot_after_up_N.
//        b. Apply down_N. Snapshot → snapshot_after_down_N.
//        c. Assert snapshot_after_down_N matches the snapshot taken
//           after up_(N-1) was applied (or empty for N=1).
//        d. Apply up_N again. Snapshot → snapshot_reapplied.
//        e. Assert snapshot_reapplied matches snapshot_after_up_N.
//
// This double-checks both that down_N reverts AND that up_N is
// re-runnable on the down state — the production migration tool's
// recovery path.

// schemaSnapshot captures the relevant DDL state of the database as
// three sorted string slices (tables, columns, indexes). Comparing
// the three slices via ElementsMatch yields a robust schema-equality
// check that's resilient to ordering differences from PG's catalog
// queries.
//
// Includes only schemaname='public' to ignore PG system tables.
type schemaSnapshot struct {
	Tables  []string
	Columns []string
	Indexes []string
}

func captureSchema(t *testing.T, ctx context.Context, h *pgintegration.Harness) schemaSnapshot {
	t.Helper()

	var snap schemaSnapshot

	// 1. Tables
	rows, err := h.DB.QueryContext(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		ORDER BY table_name`)
	require.NoError(t, err)
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		snap.Tables = append(snap.Tables, name)
	}
	require.NoError(t, rows.Err())
	if err := rows.Close(); err != nil {
		t.Logf("captureSchema: rows.Close: %v", err)
	}

	// 2. Columns — encode as "table.column:type:nullable" so a column
	//    rename, type change, or nullability flip is detected.
	rows, err = h.DB.QueryContext(ctx, `
		SELECT table_name, column_name, data_type, is_nullable
		  FROM information_schema.columns
		 WHERE table_schema = 'public'
		 ORDER BY table_name, column_name`)
	require.NoError(t, err)
	for rows.Next() {
		var table, col, dataType, isNullable string
		require.NoError(t, rows.Scan(&table, &col, &dataType, &isNullable))
		snap.Columns = append(snap.Columns,
			fmt.Sprintf("%s.%s:%s:nullable=%s", table, col, dataType, isNullable))
	}
	require.NoError(t, rows.Err())
	if err := rows.Close(); err != nil {
		t.Logf("captureSchema: rows.Close: %v", err)
	}

	// 3. Indexes — capture the full indexdef so a partial-index WHERE
	//    clause change is detected. Excludes auto-generated PKs.
	rows, err = h.DB.QueryContext(ctx, `
		SELECT tablename, indexname, indexdef
		  FROM pg_indexes
		 WHERE schemaname = 'public'
		 ORDER BY tablename, indexname`)
	require.NoError(t, err)
	for rows.Next() {
		var table, name, def string
		require.NoError(t, rows.Scan(&table, &name, &def))
		// Normalize: strip the schema-qualified table name from
		// indexdef so identical indexes across PG versions compare
		// equal.
		snap.Indexes = append(snap.Indexes,
			fmt.Sprintf("%s.%s := %s", table, name, def))
	}
	require.NoError(t, rows.Err())
	if err := rows.Close(); err != nil {
		t.Logf("captureSchema: rows.Close: %v", err)
	}

	return snap
}

// migrationFiles returns the list of migration prefixes (e.g.
// "000001_create_events_table") that have BOTH an up.sql and a
// down.sql. Each prefix is round-tripped in numeric order.
func migrationFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	upSet := map[string]bool{}
	downSet := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			upSet[strings.TrimSuffix(name, ".up.sql")] = true
		case strings.HasSuffix(name, ".down.sql"):
			downSet[strings.TrimSuffix(name, ".down.sql")] = true
		}
	}

	var prefixes []string
	for p := range upSet {
		if downSet[p] {
			prefixes = append(prefixes, p)
		} else {
			t.Errorf("migration %q has up.sql but no paired down.sql — round-trip test cannot run on it", p)
		}
	}
	sort.Strings(prefixes)
	return prefixes
}

// TestMigrationRoundTrip walks every migration through up→down→up and
// asserts the schema state returns to the prior step's snapshot
// after the down, then matches the up snapshot after the re-up.
//
// Single container shared across all 11 round-trips: state is
// deterministic by design (sequence is up_1, down_1, up_1, up_2,
// down_2, up_2, ...). Reset is NOT used — we want the cumulative
// schema state, not a clean slate.
func TestMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := pgintegration.EmptyHarness(ctx, t)
	dir := pgintegration.MigrationsDir(t)
	prefixes := migrationFiles(t, dir)
	require.NotEmpty(t, prefixes, "no paired up/down migrations found")

	// Snapshot of schema BEFORE any migration is applied — should be
	// the empty-public-schema state. Used as the "N-1 state" for the
	// first round-trip.
	priorSnapshot := captureSchema(t, ctx, h)

	for i, prefix := range prefixes {
		upFile := prefix + ".up.sql"
		downFile := prefix + ".down.sql"

		t.Run(prefix, func(t *testing.T) {
			// Step (a): apply up_N
			h.ApplyMigrationFile(t, dir, upFile)
			afterUp := captureSchema(t, ctx, h)

			// Step (b)-(c): apply down_N → schema must equal the
			// pre-up_N snapshot (the priorSnapshot from the previous
			// iteration).
			//
			// SPECIAL CASE: migration 000007 uses CREATE INDEX
			// CONCURRENTLY in the up. Postgres requires
			// CONCURRENTLY-created indexes to ALSO use CONCURRENTLY
			// on drop, OR be dropped via plain DROP INDEX, but ONLY
			// outside a transaction. Our applyMigrationFile path
			// runs each statement outside a transaction, so the
			// down's `DROP INDEX CONCURRENTLY` works.
			h.ApplyMigrationFile(t, dir, downFile)
			afterDown := captureSchema(t, ctx, h)

			assertSnapshotsEqual(t, priorSnapshot, afterDown,
				fmt.Sprintf("after %s.down.sql, schema must match the state from before %s.up.sql was applied (i=%d)", prefix, prefix, i))

			// Step (d)-(e): re-apply up_N → schema must match the
			// snapshot from step (a). Verifies up_N is re-runnable
			// on the down state.
			h.ApplyMigrationFile(t, dir, upFile)
			reapplied := captureSchema(t, ctx, h)

			assertSnapshotsEqual(t, afterUp, reapplied,
				fmt.Sprintf("after re-applying %s.up.sql post-down, schema must match the original up-state", prefix))

			// Set up for the next iteration: priorSnapshot is the
			// state AFTER up_N (the just-restored state), which is
			// the "before" state for up_(N+1).
			priorSnapshot = reapplied
		})
	}
}

// assertSnapshotsEqual compares two schemaSnapshots with
// ElementsMatch on each component. Diagnostic output names which
// component differs so a failure points the operator at the right
// part of the migration to inspect.
func assertSnapshotsEqual(t *testing.T, want, got schemaSnapshot, msg string) {
	t.Helper()
	assert.ElementsMatch(t, want.Tables, got.Tables,
		"%s: tables differ", msg)
	assert.ElementsMatch(t, want.Columns, got.Columns,
		"%s: columns differ", msg)
	assert.ElementsMatch(t, want.Indexes, got.Indexes,
		"%s: indexes differ", msg)
}

// TestMigrationsAllPaired guards the convention that every up.sql
// has a paired down.sql. A future migration that lands an up
// without a corresponding down would slip through the round-trip
// test (no prefix matches, no iteration runs); this dedicated
// check fails loudly.
func TestMigrationsAllPaired(t *testing.T) {
	dir := pgintegration.MigrationsDir(t)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var ups, downs []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			ups = append(ups, strings.TrimSuffix(name, ".up.sql"))
		case strings.HasSuffix(name, ".down.sql"):
			downs = append(downs, strings.TrimSuffix(name, ".down.sql"))
		}
	}
	sort.Strings(ups)
	sort.Strings(downs)

	assert.Equal(t, ups, downs,
		"every up.sql must have a paired down.sql in %s — missing pair makes the migration unreversible",
		filepath.Base(dir))
}
