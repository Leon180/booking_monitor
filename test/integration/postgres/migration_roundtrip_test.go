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
// four sorted string slices: tables, columns (with defaults +
// nullability), indexes (with WHERE predicates), and constraints
// (CHECK / FK / UNIQUE; PKs are subsumed by table existence).
// Comparing via ElementsMatch yields a robust schema-equality check
// resilient to ordering differences from PG's catalog queries.
//
// Includes only schema='public' to ignore PG system tables.
//
// Constraints was added in CP4c fixup after the initial review
// surfaced that the prior 3-component snapshot did not detect
// CHECK / FK constraint drift — the test's own doc comment claimed
// to catch "CHECK constraint added in up but no DROP CONSTRAINT in
// down" but couldn't, because the snapshot ignored constraints.
// 000003's existing CHECK constraint round-trip is now genuinely
// verified.
//
// column_default was added at the same time so a DROP DEFAULT
// missing from a down (uncommon today; conceivable for future
// admin-rename / partition-management migrations) doesn't slip
// through.
type schemaSnapshot struct {
	Tables      []string
	Columns     []string
	Indexes     []string
	Constraints []string
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

	// 2. Columns — encode as "table.column:type:nullable:default" so
	//    column rename, type change, nullability flip, OR default-
	//    expression drift are all detected. NULL defaults render as
	//    "<none>" so the format string is always well-formed.
	rows, err = h.DB.QueryContext(ctx, `
		SELECT table_name, column_name, data_type, is_nullable,
		       COALESCE(column_default, '<none>')
		  FROM information_schema.columns
		 WHERE table_schema = 'public'
		 ORDER BY table_name, column_name`)
	require.NoError(t, err)
	for rows.Next() {
		var table, col, dataType, isNullable, colDefault string
		require.NoError(t, rows.Scan(&table, &col, &dataType, &isNullable, &colDefault))
		snap.Columns = append(snap.Columns,
			fmt.Sprintf("%s.%s:%s:nullable=%s:default=%s", table, col, dataType, isNullable, colDefault))
	}
	require.NoError(t, rows.Err())
	if err := rows.Close(); err != nil {
		t.Logf("captureSchema: rows.Close: %v", err)
	}

	// 3. Indexes — store the indexdef verbatim. Postgres pins
	//    indexdef format per major version; this test container is
	//    pinned to postgres:15-alpine, so two runs produce
	//    byte-identical strings. If the container is bumped to
	//    postgres:16+, verify that pg_indexes.indexdef format hasn't
	//    drifted.
	rows, err = h.DB.QueryContext(ctx, `
		SELECT tablename, indexname, indexdef
		  FROM pg_indexes
		 WHERE schemaname = 'public'
		 ORDER BY tablename, indexname`)
	require.NoError(t, err)
	for rows.Next() {
		var table, name, def string
		require.NoError(t, rows.Scan(&table, &name, &def))
		snap.Indexes = append(snap.Indexes,
			fmt.Sprintf("%s.%s := %s", table, name, def))
	}
	require.NoError(t, rows.Err())
	if err := rows.Close(); err != nil {
		t.Logf("captureSchema: rows.Close: %v", err)
	}

	// 4. Constraints — captures user-named CHECK / FOREIGN KEY /
	//    UNIQUE constraints. PRIMARY KEY constraints are subsumed
	//    by table existence. The CHECK clause text from
	//    check_constraints is joined in so a semantically-changed
	//    CHECK predicate is detected even if the constraint name
	//    stays the same.
	//
	//    Filters:
	//      - cc.check_clause NOT LIKE '%IS NOT NULL': Postgres
	//        auto-generates a CHECK constraint per NOT NULL column,
	//        with an OID-based name like '2200_16457_4_not_null'.
	//        These names are NOT stable across table re-creations
	//        (the OID changes), so including them here causes
	//        false positives on round-trips that drop+recreate the
	//        table (e.g. migration 000010's ADD COLUMN ... NOT
	//        NULL → DROP COLUMN → ADD COLUMN cycle). The
	//        is_nullable='NO' attribute is already captured by the
	//        Columns snapshot, so dropping these from Constraints
	//        is non-redundant.
	rows, err = h.DB.QueryContext(ctx, `
		SELECT tc.table_name, tc.constraint_name, tc.constraint_type,
		       COALESCE(cc.check_clause, '<n/a>')
		  FROM information_schema.table_constraints tc
		  LEFT JOIN information_schema.check_constraints cc
		    ON tc.constraint_name = cc.constraint_name
		   AND tc.constraint_schema = cc.constraint_schema
		 WHERE tc.table_schema = 'public'
		   AND tc.constraint_type IN ('CHECK', 'FOREIGN KEY', 'UNIQUE')
		   AND COALESCE(cc.check_clause, '') NOT LIKE '%IS NOT NULL'
		 ORDER BY tc.table_name, tc.constraint_name`)
	require.NoError(t, err)
	for rows.Next() {
		var table, name, ctype, clause string
		require.NoError(t, rows.Scan(&table, &name, &ctype, &clause))
		snap.Constraints = append(snap.Constraints,
			fmt.Sprintf("%s.%s [%s] := %s", table, name, ctype, clause))
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
	// Symmetric check: an orphan down.sql with no paired up.sql is
	// also a sign of layout drift. The TestMigrationsAllPaired
	// guard catches this from a different angle, but warning here
	// makes the failure-mode visible during the round-trip run too.
	for p := range downSet {
		if !upSet[p] {
			t.Errorf("migration %q has down.sql but no paired up.sql — orphan down indicates layout drift", p)
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
// ElementsMatch on each of the four components. Diagnostic output
// names which component differs so a failure points the operator at
// the right part of the migration to inspect.
//
// Fail-fast via t.FailNow on any mismatch — this prevents the parent
// test's `priorSnapshot` variable from being updated to a polluted
// state when an iteration fails, which would cascade into spurious
// failures on subsequent migrations.
func assertSnapshotsEqual(t *testing.T, want, got schemaSnapshot, msg string) {
	t.Helper()
	ok := assert.ElementsMatch(t, want.Tables, got.Tables, "%s: tables differ", msg)
	ok = assert.ElementsMatch(t, want.Columns, got.Columns, "%s: columns differ", msg) && ok
	ok = assert.ElementsMatch(t, want.Indexes, got.Indexes, "%s: indexes differ", msg) && ok
	ok = assert.ElementsMatch(t, want.Constraints, got.Constraints, "%s: constraints differ", msg) && ok
	if !ok {
		t.FailNow()
	}
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
