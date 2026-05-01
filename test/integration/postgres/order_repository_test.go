//go:build integration

package pgintegration_test

import (
	"context"
	"testing"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for postgresOrderRepository against a real
// postgres:15-alpine container with all migrations applied.
//
// What these tests pin that the row-mapper unit tests
// (`order_row_test.go`) cannot:
//
//   - The CTE-based atomic state machine in `transitionStatus`. The
//     unit tests exercise neither the row-lock semantics nor the
//     `from = ANY($3)` filter that gates which transitions are legal.
//   - The order_status_history audit insert. CTE-based design means
//     the INSERT is part of the same statement as the UPDATE; if a
//     refactor split them, only an integration test catches the
//     atomicity regression.
//   - The partial unique index `uq_orders_user_event` which only
//     enforces uniqueness when `status != 'failed'`. A regression
//     that dropped the WHERE clause would silently break the
//     "user can re-buy after a failed order" contract.
//   - FindStuckCharging / FindStuckFailed query plans, including the
//     `EXTRACT(EPOCH FROM (NOW() - updated_at))` arithmetic that
//     drives the `Age` field operators read off the gauge.
//
// Each test is t.Parallel-disabled because they share a single
// container (boot is ~2s, repeated boot would dominate total runtime).
// Reset is called between tests to clear data without reapplying the
// schema.

// repoHarness boots a fresh Postgres container PER CALL (not once per
// suite — see CP4a design notes). Each top-level Test* function
// invokes this helper independently and gets its own container.
// Within a parent test, sub-tests reuse the same Harness and rely on
// h.Reset(t) between iterations to clear data without rebooting (~2s
// container boot vs <100ms truncate).
//
// CONCURRENCY CAVEAT: do NOT call t.Parallel() inside sub-tests that
// share a single Harness — Reset would race the other sub-test's
// Create. Top-level tests are isolated by construction (separate
// containers).
//
// The repo is typed as domain.OrderRepository — the production
// constructor returns an unexported concrete type, but the
// integration suite uses the interface intentionally to keep the
// tests honest about the public surface.
func repoHarness(t *testing.T) (*pgintegration.Harness, domain.OrderRepository) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewPostgresOrderRepository(h.DB)
	return h, repo
}

// seedEvent inserts a fresh event row and returns its id. Wraps
// the harness helper so individual tests get a typed UUID directly
// without touching event_id formatting.
func seedEventForOrder(t *testing.T, h *pgintegration.Harness) uuid.UUID {
	t.Helper()
	id := uuid.New()
	h.SeedEvent(t, id.String(), "Test Event", 100)
	return id
}

func newOrder(t *testing.T, eventID uuid.UUID, userID, qty int) domain.Order {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	o, err := domain.NewOrder(id, userID, eventID, qty)
	require.NoError(t, err)
	return o
}

// TestOrderRepository_CreateAndGetByID: round-trip an order via Create
// + GetByID. Verifies all fields rehydrate correctly through the row
// mapper.
func TestOrderRepository_CreateAndGetByID(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)
	o := newOrder(t, eventID, 42, 3)

	created, err := repo.Create(ctx, o)
	require.NoError(t, err)
	require.Equal(t, o.ID(), created.ID(), "Create must return the same id passed in (caller-generated)")

	got, err := repo.GetByID(ctx, o.ID())
	require.NoError(t, err)
	assert.Equal(t, o.ID(), got.ID())
	assert.Equal(t, o.UserID(), got.UserID())
	assert.Equal(t, o.EventID(), got.EventID())
	assert.Equal(t, o.Quantity(), got.Quantity())
	assert.Equal(t, domain.OrderStatusPending, got.Status())
}

// TestOrderRepository_GetByID_NotFound: missing id returns
// domain.ErrOrderNotFound, not a generic error. Pins the contract
// callers (mapError, recon, watchdog) rely on for triage.
func TestOrderRepository_GetByID_NotFound(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	_, err := repo.GetByID(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrOrderNotFound)
}

// TestOrderRepository_PartialUniqueIndex_AllowsReBuyAfterFailed:
// The `uq_orders_user_event` partial unique index only enforces
// uniqueness when status != 'failed'. A user whose first order
// failed must be able to re-attempt. Pins the WHERE clause from
// migration 000006.
func TestOrderRepository_PartialUniqueIndex_AllowsReBuyAfterFailed(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)

	// First order — pending.
	first := newOrder(t, eventID, 1, 1)
	_, err := repo.Create(ctx, first)
	require.NoError(t, err)

	// Second attempt for same user+event WHILE first is pending →
	// must violate the partial unique index. assert.ErrorIs against
	// domain.ErrUserAlreadyBought (the wrapped sentinel for pq error
	// 23505) — a generic require.Error would also accept FK or
	// connection errors and pass for the wrong reason.
	second := newOrder(t, eventID, 1, 1)
	_, err = repo.Create(ctx, second)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserAlreadyBought,
		"duplicate active order must surface ErrUserAlreadyBought (pq 23505 → domain sentinel), not any generic error")

	// Move first to Failed. Pending → Failed is a legal direct edge
	// in transitionStatus (no Charging required).
	require.NoError(t, repo.MarkFailed(ctx, first.ID()))

	// Third attempt — different uuid, same user+event, but first is
	// now 'failed' → outside the partial index predicate → MUST
	// succeed.
	third := newOrder(t, eventID, 1, 1)
	_, err = repo.Create(ctx, third)
	require.NoError(t, err, "user must be able to re-buy after a failed order")
}

// TestOrderRepository_PartialUniqueIndex_BlocksReBuyAfterCompensated
// pins the CURRENT behavior of the partial unique index `WHERE
// status != 'failed'`: a Compensated order BLOCKS re-buy (because
// 'compensated' satisfies `status != 'failed'`). This is reviewable
// as a possible spec gap — see TODO below.
//
// TODO(spec-gap): the multi-agent review of CP4a flagged that this
// behavior is unspecified in PROJECT_SPEC. The defensible argument
// for ALLOWING re-buy after Compensated: the saga compensator just
// reverted the user's inventory + refunded their payment; expecting
// them to be able to retry is intuitive. The defensible argument
// AGAINST: Compensated is terminal, the workflow expects operator
// review for the underlying failure. Pick one and document it; until
// then, this test pins the actual behavior so a refactor can't
// silently flip it.
func TestOrderRepository_PartialUniqueIndex_BlocksReBuyAfterCompensated(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)

	first := newOrder(t, eventID, 1, 1)
	_, err := repo.Create(ctx, first)
	require.NoError(t, err)

	// Walk first → Failed → Compensated.
	require.NoError(t, repo.MarkFailed(ctx, first.ID()))
	require.NoError(t, repo.MarkCompensated(ctx, first.ID()))

	// Second attempt with same user+event. The partial unique
	// index predicate `WHERE status != 'failed'` does NOT exclude
	// 'compensated', so this fails with ErrUserAlreadyBought.
	second := newOrder(t, eventID, 1, 1)
	_, err = repo.Create(ctx, second)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrUserAlreadyBought,
		"current behavior: Compensated blocks re-buy (predicate is `status != 'failed'`, not `status NOT IN ('failed', 'compensated')`). If product decides to allow re-buy, change the index AND update this test.")
}

// TestOrderRepository_StateMachine_LegalTransitions exercises the
// canonical state-graph paths through the Mark* methods. Each
// transition's success is verified via GetByID.
func TestOrderRepository_StateMachine_LegalTransitions(t *testing.T) {
	h, repo := repoHarness(t)

	type step func(domain.OrderRepository, context.Context, uuid.UUID) error
	mkCharging := func(r domain.OrderRepository, ctx context.Context, id uuid.UUID) error {
		return r.MarkCharging(ctx, id)
	}
	mkConfirmed := func(r domain.OrderRepository, ctx context.Context, id uuid.UUID) error {
		return r.MarkConfirmed(ctx, id)
	}
	mkFailed := func(r domain.OrderRepository, ctx context.Context, id uuid.UUID) error {
		return r.MarkFailed(ctx, id)
	}
	mkCompensated := func(r domain.OrderRepository, ctx context.Context, id uuid.UUID) error {
		return r.MarkCompensated(ctx, id)
	}

	tests := []struct {
		name     string
		path     []step
		wantLast domain.OrderStatus
	}{
		{
			name:     "Pending → Charging → Confirmed",
			path:     []step{mkCharging, mkConfirmed},
			wantLast: domain.OrderStatusConfirmed,
		},
		{
			name:     "Pending → Charging → Failed → Compensated",
			path:     []step{mkCharging, mkFailed, mkCompensated},
			wantLast: domain.OrderStatusCompensated,
		},
		{
			name:     "Pending → Failed (transitional direct edge)",
			path:     []step{mkFailed},
			wantLast: domain.OrderStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h.Reset(t)
			ctx := context.Background()
			eventID := seedEventForOrder(t, h)
			o := newOrder(t, eventID, 100, 1)
			_, err := repo.Create(ctx, o)
			require.NoError(t, err)

			for _, s := range tt.path {
				require.NoError(t, s(repo, ctx, o.ID()))
			}

			got, err := repo.GetByID(ctx, o.ID())
			require.NoError(t, err)
			assert.Equal(t, tt.wantLast, got.Status())
		})
	}
}

// TestOrderRepository_StateMachine_IllegalTransitions: the CTE filter
// `before.status = ANY($3)` makes illegal transitions a 0-row update,
// which transitionStatus disambiguates as ErrInvalidTransition.
func TestOrderRepository_StateMachine_IllegalTransitions(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)
	o := newOrder(t, eventID, 1, 1)
	_, err := repo.Create(ctx, o)
	require.NoError(t, err)

	// Pending → Compensated is illegal — Compensated only follows
	// Failed. The CTE returns 0 rows; transitionStatus surfaces
	// ErrInvalidTransition.
	err = repo.MarkCompensated(ctx, o.ID())
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidTransition,
		"Pending → Compensated must surface ErrInvalidTransition (compensator only runs after Failed)")

	// Status must not have been mutated.
	got, err := repo.GetByID(ctx, o.ID())
	require.NoError(t, err)
	assert.Equal(t, domain.OrderStatusPending, got.Status(),
		"failed transition must NOT mutate status")
}

// TestOrderRepository_StateMachine_HistoryRecorded: the CTE's INSERT
// branch writes to order_status_history atomically with the UPDATE.
// Verifying the history rows is the load-bearing assertion that the
// audit log isn't silently being skipped.
func TestOrderRepository_StateMachine_HistoryRecorded(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)
	o := newOrder(t, eventID, 1, 1)
	_, err := repo.Create(ctx, o)
	require.NoError(t, err)

	require.NoError(t, repo.MarkCharging(ctx, o.ID()))
	require.NoError(t, repo.MarkConfirmed(ctx, o.ID()))

	// Read back the history entries — order matters (chronological).
	rows, err := h.DB.QueryContext(ctx, `
		SELECT from_status, to_status
		  FROM order_status_history
		 WHERE order_id = $1
		 ORDER BY occurred_at ASC, id ASC`, o.ID())
	require.NoError(t, err)
	defer func() {
		if err := rows.Close(); err != nil {
			t.Logf("rows.Close: %v (non-fatal)", err)
		}
	}()

	type entry struct{ from, to string }
	var entries []entry
	for rows.Next() {
		var from, to string
		require.NoError(t, rows.Scan(&from, &to))
		entries = append(entries, entry{from, to})
	}
	require.NoError(t, rows.Err())

	require.Len(t, entries, 2, "exactly two history rows must exist after two transitions")
	assert.Equal(t, entry{"pending", "charging"}, entries[0])
	assert.Equal(t, entry{"charging", "confirmed"}, entries[1])
}

// TestOrderRepository_ListOrders_PaginationAndFilter: ListOrders
// supports limit/offset and an optional status filter. Verifies
// total-count + status filter both work; pins the contract
// /api/v1/history relies on.
func TestOrderRepository_ListOrders_PaginationAndFilter(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)

	// Seed 5 orders: 3 Pending, 2 Confirmed.
	var pendingIDs, confirmedIDs []uuid.UUID
	for i := 0; i < 5; i++ {
		// Distinct user_id so the partial unique index doesn't reject.
		o := newOrder(t, eventID, 100+i, 1)
		_, err := repo.Create(ctx, o)
		require.NoError(t, err)
		if i < 2 {
			require.NoError(t, repo.MarkCharging(ctx, o.ID()))
			require.NoError(t, repo.MarkConfirmed(ctx, o.ID()))
			confirmedIDs = append(confirmedIDs, o.ID())
		} else {
			pendingIDs = append(pendingIDs, o.ID())
		}
	}

	t.Run("no filter returns all 5", func(t *testing.T) {
		got, total, err := repo.ListOrders(ctx, 100, 0, nil)
		require.NoError(t, err)
		assert.Equal(t, 5, total)
		assert.Len(t, got, 5)
	})

	t.Run("status=confirmed returns the 2 confirmed orders by id", func(t *testing.T) {
		s := domain.OrderStatusConfirmed
		got, total, err := repo.ListOrders(ctx, 100, 0, &s)
		require.NoError(t, err)
		assert.Equal(t, 2, total, "filtered total must reflect status filter")
		require.Len(t, got, 2)
		gotIDs := make([]uuid.UUID, len(got))
		for i, o := range got {
			gotIDs[i] = o.ID()
			assert.Equal(t, domain.OrderStatusConfirmed, o.Status())
		}
		assert.ElementsMatch(t, confirmedIDs, gotIDs,
			"status filter must return EXACTLY the confirmed orders by id, not just any 2 with status='confirmed'")
	})

	t.Run("limit + offset paginate", func(t *testing.T) {
		// limit=2, offset=0 → 2 rows
		page1, total, err := repo.ListOrders(ctx, 2, 0, nil)
		require.NoError(t, err)
		assert.Equal(t, 5, total, "total must report grand-total, not page size")
		require.Len(t, page1, 2)

		// offset=4 → 1 row remaining
		page3, _, err := repo.ListOrders(ctx, 2, 4, nil)
		require.NoError(t, err)
		require.Len(t, page3, 1)

		// Pagination identity assertion: every order returned across
		// the three pages must come from the seeded set, with no
		// duplicates. Catches an ORDER BY regression that returned
		// the same row twice + missed another, which a count-only
		// assertion would miss.
		page2, _, err := repo.ListOrders(ctx, 2, 2, nil)
		require.NoError(t, err)
		require.Len(t, page2, 2)

		all := append(append(append([]uuid.UUID{}, idsOf(page1)...), idsOf(page2)...), idsOf(page3)...)
		seeded := append(append([]uuid.UUID{}, pendingIDs...), confirmedIDs...)
		assert.ElementsMatch(t, seeded, all,
			"pages 1+2+3 unioned must equal the 5 seeded ids — catches duplicate-row / missing-row pagination regressions")
	})
}

// idsOf is a small helper for the pagination identity assertion.
func idsOf(orders []domain.Order) []uuid.UUID {
	out := make([]uuid.UUID, len(orders))
	for i, o := range orders {
		out[i] = o.ID()
	}
	return out
}

// TestOrderRepository_FindStuckCharging: order in Charging older than
// minAge surfaces in the result with an Age >= minAge. Pins the
// EXTRACT(EPOCH FROM ...) arithmetic and the partial-index-driven
// query plan.
func TestOrderRepository_FindStuckCharging(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)
	o := newOrder(t, eventID, 1, 1)
	_, err := repo.Create(ctx, o)
	require.NoError(t, err)
	require.NoError(t, repo.MarkCharging(ctx, o.ID()))

	// Backdate updated_at so the row is "old" enough for the sweep.
	// Direct SQL because the production code never moves time backward.
	_, err = h.DB.ExecContext(ctx,
		`UPDATE orders SET updated_at = NOW() - interval '5 minutes' WHERE id = $1`,
		o.ID())
	require.NoError(t, err)

	// Sweep with a 1-minute threshold.
	stuck, err := repo.FindStuckCharging(ctx, 1*time.Minute, 100)
	require.NoError(t, err)
	require.Len(t, stuck, 1, "the backdated Charging order must appear in the sweep")
	assert.Equal(t, o.ID(), stuck[0].ID)
	assert.GreaterOrEqual(t, stuck[0].Age, 4*time.Minute,
		"reported Age must reflect the actual age (≥ ~5m, allowing for clock slack)")
}

// TestOrderRepository_FindStuckCharging_RespectsThreshold: a Charging
// order younger than minAge MUST NOT appear. Pins the WHERE clause's
// `updated_at < NOW() - $1::interval` predicate.
func TestOrderRepository_FindStuckCharging_RespectsThreshold(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)
	o := newOrder(t, eventID, 1, 1)
	_, err := repo.Create(ctx, o)
	require.NoError(t, err)
	require.NoError(t, repo.MarkCharging(ctx, o.ID()))

	// No backdate — order is fresh. Sweep with 1-hour threshold.
	stuck, err := repo.FindStuckCharging(ctx, 1*time.Hour, 100)
	require.NoError(t, err)
	assert.Empty(t, stuck, "fresh Charging order must not appear in 1-hour sweep")
}

// TestOrderRepository_FindStuckCharging_WrongStatusExcluded: only
// Charging orders count. A Confirmed / Failed / Compensated row
// MUST NOT appear in FindStuckCharging — excluding them is what the
// partial index buys us.
func TestOrderRepository_FindStuckCharging_WrongStatusExcluded(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)

	// Seed three orders, advance to Charging then to terminal states.
	statuses := []domain.OrderStatus{
		domain.OrderStatusConfirmed,
		domain.OrderStatusFailed,
		domain.OrderStatusCompensated,
	}
	for i, term := range statuses {
		o := newOrder(t, eventID, 200+i, 1)
		_, err := repo.Create(ctx, o)
		require.NoError(t, err)
		require.NoError(t, repo.MarkCharging(ctx, o.ID()))
		switch term {
		case domain.OrderStatusConfirmed:
			require.NoError(t, repo.MarkConfirmed(ctx, o.ID()))
		case domain.OrderStatusFailed:
			require.NoError(t, repo.MarkFailed(ctx, o.ID()))
		case domain.OrderStatusCompensated:
			require.NoError(t, repo.MarkFailed(ctx, o.ID()))
			require.NoError(t, repo.MarkCompensated(ctx, o.ID()))
		}

		// Backdate updated_at so a sweep without status filter would
		// pick them up — the filter is what excludes them.
		_, err = h.DB.ExecContext(ctx,
			`UPDATE orders SET updated_at = NOW() - interval '5 minutes' WHERE id = $1`,
			o.ID())
		require.NoError(t, err)
	}

	stuck, err := repo.FindStuckCharging(ctx, 1*time.Minute, 100)
	require.NoError(t, err)
	assert.Empty(t, stuck,
		"FindStuckCharging must exclude Confirmed / Failed / Compensated even when backdated")
}

// TestOrderRepository_FindStuckFailed: symmetric counterpart to
// TestOrderRepository_FindStuckCharging — a stale Failed order
// surfaces with the right age. Pins the same partial index from
// migration 000011 (which widened the predicate to include 'failed').
func TestOrderRepository_FindStuckFailed(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)
	o := newOrder(t, eventID, 1, 1)
	_, err := repo.Create(ctx, o)
	require.NoError(t, err)
	require.NoError(t, repo.MarkFailed(ctx, o.ID()))

	_, err = h.DB.ExecContext(ctx,
		`UPDATE orders SET updated_at = NOW() - interval '3 minutes' WHERE id = $1`,
		o.ID())
	require.NoError(t, err)

	stuck, err := repo.FindStuckFailed(ctx, 30*time.Second, 100)
	require.NoError(t, err)
	require.Len(t, stuck, 1)
	assert.Equal(t, o.ID(), stuck[0].ID)
	assert.GreaterOrEqual(t, stuck[0].Age, 2*time.Minute)
}

// TestOrderRepository_FindStuckFailed_OnlyFailedStatus: Compensated
// orders MUST NOT appear in FindStuckFailed even if they were recently
// in Failed state. Verifies the saga-watchdog won't re-drive an
// already-compensated order, which would trigger a phantom revert.
func TestOrderRepository_FindStuckFailed_OnlyFailedStatus(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)
	o := newOrder(t, eventID, 1, 1)
	_, err := repo.Create(ctx, o)
	require.NoError(t, err)
	require.NoError(t, repo.MarkFailed(ctx, o.ID()))
	require.NoError(t, repo.MarkCompensated(ctx, o.ID()))

	_, err = h.DB.ExecContext(ctx,
		`UPDATE orders SET updated_at = NOW() - interval '5 minutes' WHERE id = $1`,
		o.ID())
	require.NoError(t, err)

	stuck, err := repo.FindStuckFailed(ctx, 30*time.Second, 100)
	require.NoError(t, err)
	assert.Empty(t, stuck,
		"Compensated orders must NOT appear — preventing phantom revert is the watchdog's load-bearing safety property")
}

// TestOrderRepository_FindStuckCharging_LimitRespected: when more
// stuck rows exist than `limit`, the result is capped. Pins the
// LIMIT $2 clause; without it, a backlog spike could produce a giant
// allocation in the sweep handler.
func TestOrderRepository_FindStuckCharging_LimitRespected(t *testing.T) {
	h, repo := repoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	eventID := seedEventForOrder(t, h)

	// Seed 5 stuck-Charging orders, all backdated.
	for i := 0; i < 5; i++ {
		o := newOrder(t, eventID, 300+i, 1)
		_, err := repo.Create(ctx, o)
		require.NoError(t, err)
		require.NoError(t, repo.MarkCharging(ctx, o.ID()))
		_, err = h.DB.ExecContext(ctx,
			`UPDATE orders SET updated_at = NOW() - interval '5 minutes' WHERE id = $1`,
			o.ID())
		require.NoError(t, err)
	}

	// Sweep with limit=2 → at most 2 rows.
	stuck, err := repo.FindStuckCharging(ctx, 1*time.Minute, 2)
	require.NoError(t, err)
	assert.Len(t, stuck, 2,
		"LIMIT must cap the result at the requested batch size")
}
