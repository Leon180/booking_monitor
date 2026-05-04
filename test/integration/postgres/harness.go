//go:build integration

// Package pgintegration is a shared testcontainers harness for the
// postgres persistence-layer integration suite (CP4a).
//
// Why this package exists: the postgres repositories
// (`internal/infrastructure/persistence/postgres/*.go`) are tested
// today only at the row-mapper level (`order_row_test.go`,
// `outbox_row_test.go`) — table-driven unit tests that don't exercise
// real SQL. That leaves the SQL itself untested: the partial indexes
// from migrations 000010 / 000011, the CTE-based atomic state
// transitions in `transitionStatus`, the `EXTRACT(EPOCH FROM ...)`
// time arithmetic in `FindStuckCharging` / `FindStuckFailed`, and the
// schema's invariants (FK constraints, unique constraints, NOT NULL).
// All of those CAN regress in ways no row-mapper test catches.
//
// The harness spins up a fresh `postgres:15-alpine` testcontainer per
// test (or per parent test with sub-tests), applies every migration
// in `deploy/postgres/migrations/*.up.sql` in numeric order, and
// returns a `*sql.DB` ready for the repos under test.
//
// Build-tagged `integration` so `go test ./...` from CI's unit-test
// job does NOT trigger Docker pulls. The dedicated `make
// test-integration` target (and its CI counterpart, when added)
// passes `-tags=integration` to opt in.
package pgintegration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Harness wraps the live testcontainer + migrated *sql.DB. The
// container is auto-terminated via t.Cleanup so individual tests
// don't have to remember to call Close.
type Harness struct {
	Container *tcpostgres.PostgresContainer
	DB        *sql.DB
	DSN       string
}

// migrationsDir resolves to deploy/postgres/migrations/ regardless of
// whether the test is run from the repo root or a sub-package. Walks
// up from the harness file's directory looking for the canonical
// path; fails the test loudly if not found so a misconfigured layout
// can't silently apply zero migrations.
//
// Hardcoded relative path because Go test working directory is
// always the package directory of the test under execution
// (test/integration/postgres/), so the path "../../../deploy/..."
// is deterministic.
func migrationsDir(t *testing.T) string {
	t.Helper()
	const rel = "../../../deploy/postgres/migrations"
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("migrationsDir: filepath.Abs(%q): %v", rel, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("migrationsDir: %q not accessible — repo layout drift? %v", abs, err)
	}
	return abs
}

// StartPostgres boots a fresh postgres:15-alpine testcontainer,
// applies every up-migration, and returns a Harness. Container is
// terminated via t.Cleanup. Database is `booking_test` to avoid
// confusion with any local Postgres on the developer's machine.
//
// The boot waits for the canonical "ready to accept connections"
// log line (twice — Postgres prints it once during init, once after
// final startup); 60s is the generous-but-not-flaky timeout that
// matches testcontainers-go's recommended pattern.
func StartPostgres(ctx context.Context, t *testing.T) *Harness {
	t.Helper()

	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:15-alpine",
		tcpostgres.WithDatabase("booking_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("StartPostgres: container start: %v", err)
	}

	t.Cleanup(func() {
		// Use a fresh ctx for cleanup — the test's ctx may already be
		// cancelled if the test failed via t.Fatal.
		ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := pgContainer.Terminate(ctx2); err != nil {
			t.Logf("StartPostgres: container terminate: %v (non-fatal)", err)
		}
	})

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("StartPostgres: connection string: %v", err)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("StartPostgres: sql.Open: %v", err)
	}
	t.Cleanup(func() {
		// Close errors are non-fatal but worth logging — a leaked
		// connection during cleanup is the kind of thing that
		// silently degrades a CI host's docker daemon over a long
		// run.
		if err := db.Close(); err != nil {
			t.Logf("StartPostgres: db.Close: %v (non-fatal)", err)
		}
	})

	// PingContext with retry — testcontainers-go's wait strategy
	// already verifies "ready to accept connections" but the *sql.DB
	// pool needs its own first-connect verification before the test
	// uses it. 30 attempts at 100ms = 3s budget; on a healthy host
	// the first attempt usually succeeds.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pingWithRetry(pingCtx, db, 30, 100*time.Millisecond); err != nil {
		t.Fatalf("StartPostgres: ping: %v", err)
	}

	if err := applyMigrations(ctx, db, migrationsDir(t)); err != nil {
		t.Fatalf("StartPostgres: applyMigrations: %v", err)
	}

	return &Harness{
		Container: pgContainer,
		DB:        db,
		DSN:       dsn,
	}
}

// pingWithRetry is a small backoff helper because the *sql.DB pool's
// initial Ping can race with the container's network setup even after
// the wait-for-log strategy returns.
func pingWithRetry(ctx context.Context, db *sql.DB, attempts int, delay time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := db.PingContext(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("ping: ctx done: %w (last db err: %v)", ctx.Err(), lastErr)
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("ping: %d attempts exhausted: %w", attempts, lastErr)
}

// applyMigrations reads every *.up.sql in dir, sorts them by filename
// (which preserves the 000001_, 000002_, ... prefix ordering), and
// executes each via the PostgreSQL simple query protocol — lib/pq
// sends the full file contents as a single ExecContext call, and
// Postgres executes ALL semicolon-separated statements within it
// (migration 000008 has 7 statements, 000009 has 3, etc.). This
// bypasses the migration version table on purpose: testcontainer
// DBs are transient, so version tracking would be cargo-cult.
//
// CRITICAL DEPENDENCY ON lib/pq's simple protocol: a switch to pgx
// v5 (extended/prepared protocol by default) would silently apply
// only the FIRST statement per file, leaving indexes / constraints
// missing. The harness_test.go smoke test asserts on a sample of
// indexes from the multi-statement migrations as a tripwire.
//
// We deliberately do NOT shell out to the `migrate` CLI: that would
// require `migrate` to be installed in the test environment (works on
// the developer laptop but breaks CI Docker images that don't carry
// it). Plain SQL exec keeps the harness self-contained.
func applyMigrations(ctx context.Context, db *sql.DB, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}

	var upFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".up.sql") {
			upFiles = append(upFiles, e.Name())
		}
	}
	sort.Strings(upFiles)

	if len(upFiles) == 0 {
		return fmt.Errorf("no .up.sql files in %q — repo layout drift?", dir)
	}

	for _, name := range upFiles {
		path := filepath.Join(dir, name)
		bytes, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
		if _, err := db.ExecContext(ctx, string(bytes)); err != nil {
			return fmt.Errorf("apply %q: %w", name, err)
		}
	}
	return nil
}

// ApplyMigrationFile runs a single migration file against the harness's
// DB. Used by the round-trip test (CP4c) which needs to apply
// individual up/down files in a controlled sequence rather than the
// full forward replay that StartPostgres does.
//
// Uses ExecContext (not Exec) for consistency with applyMigrations
// and to respect the test's context cancellation budget. Same
// simple-protocol multi-statement reliance (see applyMigrations'
// comment for the lib/pq dependency note).
func (h *Harness) ApplyMigrationFile(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ApplyMigrationFile: read %q: %v", path, err)
	}
	if _, err := h.DB.ExecContext(t.Context(), string(bytes)); err != nil {
		t.Fatalf("ApplyMigrationFile: exec %q: %v", name, err)
	}
}

// MigrationsDir returns the absolute path to the project's migrations
// directory. Exported so the round-trip test can pass individual file
// names to ApplyMigrationFile without re-resolving the path.
func MigrationsDir(t *testing.T) string {
	t.Helper()
	return migrationsDir(t)
}

// EmptyHarness returns a Harness backed by a fresh container with NO
// migrations applied — used by the round-trip test which drives
// migrations explicitly, file-by-file. The CP4a/4b functional tests
// continue to use StartPostgres which auto-applies the full forward
// sequence.
func EmptyHarness(ctx context.Context, t *testing.T) *Harness {
	t.Helper()
	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:15-alpine",
		tcpostgres.WithDatabase("booking_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("EmptyHarness: container start: %v", err)
	}
	t.Cleanup(func() {
		ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := pgContainer.Terminate(ctx2); err != nil {
			t.Logf("EmptyHarness: container terminate: %v (non-fatal)", err)
		}
	})

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("EmptyHarness: connection string: %v", err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("EmptyHarness: sql.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Logf("EmptyHarness: db.Close: %v (non-fatal)", err)
		}
	})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pingWithRetry(pingCtx, db, 30, 100*time.Millisecond); err != nil {
		t.Fatalf("EmptyHarness: ping: %v", err)
	}

	return &Harness{Container: pgContainer, DB: db, DSN: dsn}
}

// Reset truncates every mutable test table so a single Harness can be
// reused across multiple parent tests / subtests without rebooting
// the container (container boot is ~2-3 s; TRUNCATE is sub-100 ms).
//
// CASCADE is required because order_status_history has a FK on
// orders, and mutual references appear elsewhere; CASCADE
// short-circuits the dependency analysis. RESTART IDENTITY resets
// any SERIAL sequences (not currently used by these tables — they
// use UUIDs — but kept defensive in case a future table adopts SERIAL).
//
// Schema migrations are NOT undone — only data is cleared.
//
// MAINTENANCE NOTE: the table list below is a manually maintained
// allowlist. When CP4b adds tests for new aggregates, OR when a
// future migration introduces a new mutable table, this list MUST
// be extended — otherwise data leaks silently between tests
// (passing tests for the wrong reason). A schema-driven
// `pg_tables`-derived TRUNCATE was considered and rejected: pulling
// the table list at runtime would also truncate any audit /
// schema_migrations tables a future tool installs, which we may
// want to preserve.
func (h *Harness) Reset(t *testing.T) {
	t.Helper()
	const stmt = `TRUNCATE TABLE order_status_history, orders, events_outbox, events RESTART IDENTITY CASCADE`
	if _, err := h.DB.Exec(stmt); err != nil {
		t.Fatalf("Harness.Reset: %v", err)
	}
}

// SeedEvent inserts a row directly into events with a known id +
// total_tickets, returning the id. Used by tests that need a foreign-
// key target for orders without going through EventRepository (which
// is itself under test in CP4b).
func (h *Harness) SeedEvent(t *testing.T, id, name string, totalTickets int) {
	t.Helper()
	const stmt = `
		INSERT INTO events (id, name, total_tickets, available_tickets, version)
		VALUES ($1::uuid, $2, $3, $3, 0)`
	if _, err := h.DB.Exec(stmt, id, name, totalTickets); err != nil {
		t.Fatalf("Harness.SeedEvent: %v", err)
	}
}

// SeedOrder inserts a row directly into orders bypassing OrderRepository
// — used by CP4b's UoW + Outbox tests that need a pre-existing order
// row as a fixture without paying the cost of a Create call. The
// order's foreign-key target (eventID) must already exist.
//
// Default status is 'pending'; pass an explicit status string to
// override (e.g. "charging" / "failed" for tests targeting specific
// branches). user_id and quantity default to 1.
//
// D4.1 caveat: this helper produces a row WITHOUT `ticket_type_id`,
// `amount_cents`, or `currency` (all NULLable post-000014 — the row
// represents a pre-D4.1 / migration-gap state). Tests that exercise
// the post-D4.1 payment path (`/pay`) against orders inserted via
// `SeedOrder` will hit `payment.ErrOrderMissingPriceSnapshot` from
// the defensive `HasPriceSnapshot()` guard. For "real" D4.1
// reservations use `SeedReservation` below.
func (h *Harness) SeedOrder(t *testing.T, id, eventID, status string) {
	t.Helper()
	if status == "" {
		status = "pending"
	}
	const stmt = `
		INSERT INTO orders (id, event_id, user_id, quantity, status, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, 1, 1, $3, NOW(), NOW())`
	if _, err := h.DB.Exec(stmt, id, eventID, status); err != nil {
		t.Fatalf("Harness.SeedOrder: %v", err)
	}
}

// SeedReservation inserts a Pattern A reservation row complete with
// the D4.1 price snapshot (`ticket_type_id` + `amount_cents` +
// `currency` populated; `reserved_until` set to 15 minutes in the
// future). Use this for tests that need an order capable of
// completing the `/pay` flow — `payment.Service.CreatePaymentIntent`
// rejects orders missing the snapshot via `ErrOrderMissingPriceSnapshot`,
// so tests using `SeedOrder` will silently fail that path.
//
// status defaults to 'awaiting_payment' (the canonical Pattern A entry
// state). Pass a non-empty value to override.
func (h *Harness) SeedReservation(t *testing.T, id, eventID, ticketTypeID, status string, amountCents int64, currency string) {
	t.Helper()
	if status == "" {
		status = "awaiting_payment"
	}
	const stmt = `
		INSERT INTO orders (
			id, event_id, ticket_type_id, user_id, quantity, status,
			created_at, updated_at, reserved_until,
			amount_cents, currency
		)
		VALUES (
			$1::uuid, $2::uuid, $3::uuid, 1, 1, $4,
			NOW(), NOW(), NOW() + INTERVAL '15 minutes',
			$5, $6
		)`
	if _, err := h.DB.Exec(stmt, id, eventID, ticketTypeID, status, amountCents, currency); err != nil {
		t.Fatalf("Harness.SeedReservation: %v", err)
	}
}
