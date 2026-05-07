//go:build integration

package pgintegration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for D6 — `FindExpiredReservations` +
// `CountOverdueAfterCutoff` against a real postgres:15-alpine
// container with all migrations applied.
//
// What these tests pin that the unit tests cannot:
//
//   - The DB-NOW SQL semantics (`reserved_until <= NOW() - $1::interval`)
//     under real Postgres time arithmetic. Round-2 F1 fix moved the
//     cutoff from app-clock to DB-clock; the repo unit tests can't
//     prove this — they'd need a real `NOW()` source.
//   - The partial index `idx_orders_awaiting_payment_reserved_until`
//     from migration 000012 actually exists with the expected
//     predicate, AND is reachable for D6's query shape under
//     `enable_seqscan=off` (round-1 P2 + round-3 F1 EXPLAIN-flake fix:
//     don't assert planner cost decisions on small CI data; assert
//     index existence + reachability separately).
//   - The interval-string parameter formatting (`%f seconds`) doesn't
//     drift between Go-side `time.Duration.Seconds()` and Postgres's
//     `INTERVAL` parser. A round-trip integration is the only place
//     this is exercised end-to-end.
//   - 4 row-class state-machine assertions: only
//     `(awaiting_payment, past)` rows surface; everything else
//     (paid/expired/future-reservation) is correctly filtered.

// d6Repo boots a fresh Postgres container per call (mirrors
// repoHarness). Returns the harness + repo for D6's specific methods.
func d6Repo(t *testing.T) (*pgintegration.Harness, domain.OrderRepository) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewPostgresOrderRepository(h.DB)
	return h, repo
}

// seedReservationWithPast inserts an awaiting_payment order whose
// reserved_until is `pastBy` ago. We can't go through
// `domain.NewReservation` (it rejects past times); use Reconstruct +
// Create directly to model the production scenario where
// BookingService.BookTicket created the row with a future
// reserved_until and the wall-clock just elapsed.
func seedReservationWithPast(
	t *testing.T, h *pgintegration.Harness, eventID uuid.UUID, userID int, pastBy time.Duration,
) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	repo := postgres.NewPostgresOrderRepository(h.DB)

	reservedUntil := time.Now().Add(-pastBy).UTC()
	createdAt := reservedUntil.Add(-15 * time.Minute) // before reserved_until

	ttID, err := uuid.NewV7()
	require.NoError(t, err)
	// payment_intent_id intentionally empty — D5's
	// orders_payment_intent_id_unique_idx (migration 000015) is a
	// partial unique on `WHERE payment_intent_id IS NOT NULL`, so an
	// empty string (mapped to SQL NULL by the row mapper) doesn't
	// participate in the uniqueness constraint. Models the realistic
	// "customer never reached /pay" expiry case the sweeper is for.
	o := domain.ReconstructOrder(
		id, userID, eventID, ttID, 1,
		domain.OrderStatusAwaitingPayment,
		createdAt, reservedUntil,
		"", 2000, "usd",
	)
	_, err = repo.Create(context.Background(), o)
	require.NoError(t, err)
	return id
}

// seedReservationWithFuture inserts an awaiting_payment order with a
// future reserved_until — D6 must NOT pick it up.
func seedReservationWithFuture(
	t *testing.T, h *pgintegration.Harness, eventID uuid.UUID, userID int,
) uuid.UUID {
	t.Helper()
	o := newReservation(t, eventID, userID, 1)
	repo := postgres.NewPostgresOrderRepository(h.DB)
	_, err := repo.Create(context.Background(), o)
	require.NoError(t, err)
	return o.ID()
}

// ── correctness ─────────────────────────────────────────────────────

// TestFindExpiredReservations_OnlyReturnsAwaitingPaymentPastCutoff:
// the central correctness assertion. 4 row classes; only the past-
// awaiting_payment row surfaces.
func TestFindExpiredReservations_OnlyReturnsAwaitingPaymentPastCutoff(t *testing.T) {
	h, repo := d6Repo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)
	ctx := context.Background()

	// 1. (awaiting_payment, past) — eligible, expect to surface.
	pastID := seedReservationWithPast(t, h, eventID, 1, 1*time.Minute)

	// 2. (awaiting_payment, future) — NOT eligible. Distinct user_id
	// because of the `uq_orders_user_event` partial unique index
	// (no two non-failed orders for the same (user_id, event_id)).
	_ = seedReservationWithFuture(t, h, eventID, 2)

	// 3. (paid, past) — D5 won the race. NOT eligible.
	paidID := seedReservationWithPast(t, h, eventID, 3, 1*time.Minute)
	_, err := h.DB.Exec(`UPDATE orders SET status='paid' WHERE id=$1`, paidID)
	require.NoError(t, err)

	// 4. (expired, past) — already swept by a previous tick. NOT eligible (idempotency).
	expiredID := seedReservationWithPast(t, h, eventID, 4, 1*time.Minute)
	_, err = h.DB.Exec(`UPDATE orders SET status='expired' WHERE id=$1`, expiredID)
	require.NoError(t, err)

	rows, err := repo.FindExpiredReservations(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "only the (awaiting_payment, past) row should surface")
	assert.Equal(t, pastID, rows[0].ID)
	assert.Greater(t, rows[0].Age, 30*time.Second,
		"Age should reflect NOW() - reserved_until (>30s for a row inserted 1m past expiry)")
}

// seedReservationWithPast helper override that takes the Harness in
// the order seedReservationWithPast above doesn't (closure-captures).
// Adapter so the test calls don't drift across the suite.

// TestFindExpiredReservations_GracePeriodHonored: rows at the boundary
// of `reserved_until <= NOW() - grace` are correctly filtered.
func TestFindExpiredReservations_GracePeriodHonored(t *testing.T) {
	h, repo := d6Repo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)

	// Row: reserved_until = NOW() - 3s (in the past, but only by 3s).
	// With grace=5s, the cutoff is NOW() - 5s, so this row is NOT
	// past the cutoff (it's only 3s past, the predicate wants ≥ 5s
	// past). With grace=0, this row IS past the cutoff.
	id := seedReservationWithPast(t, h, eventID, 1, 3*time.Second)

	rows, err := repo.FindExpiredReservations(context.Background(), 5*time.Second, 10)
	require.NoError(t, err)
	require.Empty(t, rows, "grace=5s should exclude a row only 3s past reserved_until")

	rows, err = repo.FindExpiredReservations(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "grace=0 should include the same row (3s past NOW())")
	assert.Equal(t, id, rows[0].ID)
}

// TestFindExpiredReservations_OldestFirstOrdering: SQL ORDER BY
// `reserved_until ASC`. Verify the sweeper sees the oldest-first
// ordering its observability gauges depend on.
func TestFindExpiredReservations_OldestFirstOrdering(t *testing.T) {
	h, repo := d6Repo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)

	// Three rows, increasing distance from NOW().
	youngID := seedReservationWithPast(t, h, eventID, 1, 5*time.Second)
	midID := seedReservationWithPast(t, h, eventID, 2, 30*time.Second)
	oldID := seedReservationWithPast(t, h, eventID, 3, 5*time.Minute)

	rows, err := repo.FindExpiredReservations(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, oldID, rows[0].ID, "oldest reserved_until first")
	assert.Equal(t, midID, rows[1].ID)
	assert.Equal(t, youngID, rows[2].ID)
}

// TestFindExpiredReservations_LimitRespected: BatchSize knob bounds
// per-tick scan size.
func TestFindExpiredReservations_LimitRespected(t *testing.T) {
	h, repo := d6Repo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)

	for i := 0; i < 5; i++ {
		seedReservationWithPast(t, h, eventID, i+1, 1*time.Minute)
	}

	rows, err := repo.FindExpiredReservations(context.Background(), 0, 3)
	require.NoError(t, err)
	require.Len(t, rows, 3, "LIMIT $2 must cap result count")
}

// ── post-sweep observability query ──────────────────────────────────

// TestCountOverdueAfterCutoff_HappyAndEmpty: count returns 0 when no
// rows are overdue, plus the count + oldestAge correctly reflect a
// populated state.
func TestCountOverdueAfterCutoff_HappyAndEmpty(t *testing.T) {
	h, repo := d6Repo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)
	ctx := context.Background()

	// Empty state.
	count, oldest, err := repo.CountOverdueAfterCutoff(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
	assert.Equal(t, time.Duration(0), oldest, "oldestAge MUST be 0 (not NULL) when count is 0")

	// Seed 3 overdue rows. Oldest is 5min past; expect oldest ≈ 5min.
	seedReservationWithPast(t, h, eventID, 1, 30*time.Second)
	seedReservationWithPast(t, h, eventID, 2, 1*time.Minute)
	seedReservationWithPast(t, h, eventID, 3, 5*time.Minute)

	count, oldest, err = repo.CountOverdueAfterCutoff(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
	assert.Greater(t, oldest, 4*time.Minute,
		"oldestAge should track the row at NOW() - 5min, not the median")
}

// ── round-1 P2 + round-3 F1: index reachability (NOT planner cost) ──

// TestExpiryIndex_Exists: the partial unique index 000012 named for
// D6 must exist with the expected predicate. Deterministic; doesn't
// depend on planner cost decisions.
func TestExpiryIndex_Exists(t *testing.T) {
	h, _ := d6Repo(t)
	h.Reset(t)

	const q = `
		SELECT indexname, indexdef
		  FROM pg_indexes
		 WHERE schemaname = 'public'
		   AND tablename = 'orders'
		   AND indexname = 'idx_orders_awaiting_payment_reserved_until'`
	var name, def string
	err := h.DB.QueryRow(q).Scan(&name, &def)
	require.NoError(t, err, "migration 000012 partial index must exist")
	assert.Equal(t, "idx_orders_awaiting_payment_reserved_until", name)
	assert.Contains(t, def, "WHERE", "must be a partial index (WHERE clause present)")
	assert.Contains(t, def, "awaiting_payment", "predicate must filter to awaiting_payment status")
	assert.Contains(t, def, "reserved_until", "must include reserved_until column")
}

// TestExpiryIndex_PlannerCanReachIt: under SET enable_seqscan=off,
// EXPLAIN must show an Index Scan using the D6 partial index. This
// proves the index IS reachable for the query shape; planner cost
// decisions on small CI data are NOT what we test here. Round-1 P2 +
// round-3 F1 EXPLAIN-flake fix.
func TestExpiryIndex_PlannerCanReachIt(t *testing.T) {
	h, _ := d6Repo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)

	// Seed a row so the planner has something to actually hit.
	seedReservationWithPast(t, h, eventID, 1, 1*time.Minute)

	// Force the planner away from Seq Scan so we see what the index
	// path actually does on this query shape, regardless of CI data
	// size. lib/pq forbids multi-statement prepared queries; split
	// SET LOCAL + EXPLAIN into two round-trips within the same tx.
	tx, err := h.DB.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)

	const explainQ = `EXPLAIN (FORMAT TEXT)
		SELECT id, EXTRACT(EPOCH FROM (NOW() - reserved_until))
		  FROM orders
		 WHERE status = 'awaiting_payment'
		   AND reserved_until <= NOW() - $1::interval
		 ORDER BY reserved_until ASC
		 LIMIT $2`

	rows, err := tx.Query(explainQ, "0 seconds", 10)
	require.NoError(t, err, "EXPLAIN under enable_seqscan=off must execute")
	defer func() { _ = rows.Close() }()

	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line + "\n")
	}
	require.NoError(t, rows.Err())

	planText := plan.String()
	t.Logf("EXPLAIN plan under enable_seqscan=off:\n%s", planText)
	assert.Contains(t, planText, "idx_orders_awaiting_payment_reserved_until",
		"under enable_seqscan=off, the D6 query MUST be reachable via the migration 000012 partial index")
}
