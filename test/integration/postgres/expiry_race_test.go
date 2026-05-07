//go:build integration

package pgintegration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Repo-layer race tests for D6 — concurrent state-machine writes
// against the SAME order row.
//
// Round-3 F3 split: this file ONLY exercises repo SQL outcomes
// (MarkExpired vs MarkPaid / MarkPaymentFailed). The webhook-handler
// behavior on the corresponding race (signed `succeeded` envelope on
// a D6-expired row → `handleLateSuccess` + `late_success_total`) is
// covered by `TestHandleWebhook_LateSuccess_PostTerminal_SucceededOnExpired`
// from PR #92 — D6 plan does NOT re-test that path.
//
// The point of these tests: D5's `MarkPaid` SQL has a
// `reserved_until > NOW()` predicate (the round-2 F1 fix that ensures
// D5 + D6 share DB-NOW as a single time source); so for D6-eligible
// rows (`reserved_until <= NOW()`), `MarkPaid` cannot succeed. We
// prove this end-to-end against real Postgres concurrency:
// `MarkExpired` and `MarkPaid` race on the same row, exactly one
// transitions, the other returns the expected sentinel.

// d6RaceRepo boots a fresh container per test. Each test is otherwise
// short-lived; we accept the ~2s container boot cost for the
// race-isolation benefit (parallel goroutines on a shared harness
// across tests would interfere).
func d6RaceRepo(t *testing.T) (*pgintegration.Harness, domain.OrderRepository) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewPostgresOrderRepository(h.DB)
	return h, repo
}

// raceSeedPastReservation is the race-test counterpart of
// seedReservationWithPast. Returns the order id; the row is in
// awaiting_payment with reserved_until in the past (eligible for D6
// AND ineligible for D5's MarkPaid by the SQL predicate).
func raceSeedPastReservation(
	t *testing.T, h *pgintegration.Harness, eventID uuid.UUID, userID int,
) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	repo := postgres.NewPostgresOrderRepository(h.DB)

	reservedUntil := time.Now().Add(-1 * time.Minute).UTC()
	createdAt := reservedUntil.Add(-15 * time.Minute)

	ttID, err := uuid.NewV7()
	require.NoError(t, err)
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

// TestRace_MarkExpiredVsMarkPaid: spawn two goroutines both targeting
// the same overdue row. One calls MarkExpired (D6's path); the other
// calls MarkPaid (a D5 webhook handler simulation, but ONLY at the
// repo layer — no service-layer assertions, see file header).
//
// Expected outcome under DB-NOW SQL predicates:
//   - MarkExpired succeeds (status: awaiting_payment → expired).
//   - MarkPaid returns ErrInvalidTransition. With round-2 F1, MarkPaid's
//     SQL predicate is `WHERE status='awaiting_payment' AND reserved_until > NOW()`.
//     A row with reserved_until=NOW()-1min FAILS the predicate even
//     before D6 runs. So this test pins TWO things:
//       1. Concurrent transitions don't both succeed (one rows-affected wins).
//       2. MarkPaid CANNOT succeed against a D6-eligible row regardless
//          of who runs first — the predicate is the contract.
//
// Run with `-race`. Single iteration is enough: Postgres serialises
// row writes via the FOR UPDATE inside `transitionStatus`, so the
// "one wins, one loses" outcome is structural, not statistical.
func TestRace_MarkExpiredVsMarkPaid(t *testing.T) {
	h, repo := d6RaceRepo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)
	orderID := raceSeedPastReservation(t, h, eventID, 1)
	ctx := context.Background()

	var (
		wg            sync.WaitGroup
		expiredErr    error
		paidErr       error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		expiredErr = repo.MarkExpired(ctx, orderID)
	}()
	go func() {
		defer wg.Done()
		paidErr = repo.MarkPaid(ctx, orderID)
	}()
	wg.Wait()

	// MarkExpired must succeed (status was awaiting_payment, no
	// reserved_until guard on MarkExpired — Codex round-1 P1 contract).
	// `require.NoError` (NOT `assert.NoError`) — without it, a both-
	// fail scenario (e.g. concurrent DB blip kills both goroutines)
	// would silently flow into the "MarkPaid failed as expected"
	// branch below, masking the regression. require.NoError fails
	// fast and prevents the misleading downstream check.
	require.NoError(t, expiredErr, "MarkExpired must succeed against awaiting_payment regardless of reserved_until")

	// MarkPaid must fail. Two distinct error shapes are acceptable
	// depending on whether MarkExpired won the row first or whether
	// MarkPaid's own SQL predicate (reserved_until > NOW()) caught
	// the row first. Both paths are valid — the contract is "MarkPaid
	// MUST NOT succeed":
	//   - If MarkPaid won the row lock first: status was still
	//     awaiting_payment but `reserved_until > NOW()` is false →
	//     classifyPaidGuardFailure → ErrReservationExpired.
	//   - If MarkExpired won first: status is now expired →
	//     classifyPaidGuardFailure → ErrInvalidTransition.
	require.Error(t, paidErr, "MarkPaid MUST NOT succeed on a D6-eligible row")
	assert.True(t,
		errors.Is(paidErr, domain.ErrReservationExpired) ||
			errors.Is(paidErr, domain.ErrInvalidTransition),
		"expected ErrReservationExpired (predicate caught it) or ErrInvalidTransition (D6 won row); got %v", paidErr)

	// Verify final DB state: status=expired, history row written.
	var status string
	require.NoError(t, h.DB.QueryRow(`SELECT status FROM orders WHERE id=$1`, orderID).Scan(&status))
	assert.Equal(t, "expired", status, "final state must be expired (D6 won)")

	var historyCount int
	require.NoError(t, h.DB.QueryRow(`
		SELECT count(*) FROM order_status_history
		 WHERE order_id=$1 AND from_status='awaiting_payment' AND to_status='expired'
	`, orderID).Scan(&historyCount))
	assert.Equal(t, 1, historyCount, "exactly one awaiting_payment→expired history row")
}

// TestRace_MarkExpiredVsMarkPaymentFailed: the symmetric race. D5's
// `payment_intent.payment_failed` webhook arrives concurrently with
// D6's sweep tick.
//
// MarkPaymentFailed has NO reserved_until guard (failures should
// proceed regardless of TTL — refunds via saga, not D6). So either
// transition COULD win. Whichever loses returns ErrInvalidTransition.
//
// Plan v4 §E case 2: "D5 payment_failed wins, then D6 sweep" → D6
// outcome=already_terminal. This race test pins the repo half of that
// outcome (the application-layer outcome label is in the unit test
// `TestSweep_AlreadyTerminal_UoWErrInvalidTransition`).
func TestRace_MarkExpiredVsMarkPaymentFailed(t *testing.T) {
	h, repo := d6RaceRepo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)
	orderID := raceSeedPastReservation(t, h, eventID, 1)
	ctx := context.Background()

	var (
		wg            sync.WaitGroup
		expiredErr    error
		failedErr     error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		expiredErr = repo.MarkExpired(ctx, orderID)
	}()
	go func() {
		defer wg.Done()
		failedErr = repo.MarkPaymentFailed(ctx, orderID)
	}()
	wg.Wait()

	// Exactly one must succeed; the other gets ErrInvalidTransition.
	successCount := 0
	if expiredErr == nil {
		successCount++
	}
	if failedErr == nil {
		successCount++
	}
	require.Equal(t, 1, successCount, "exactly one transition must succeed; the other gets ErrInvalidTransition")

	if expiredErr != nil {
		assert.ErrorIs(t, expiredErr, domain.ErrInvalidTransition,
			"if MarkPaymentFailed won, MarkExpired must return ErrInvalidTransition (D6 already_terminal path)")
	}
	if failedErr != nil {
		assert.ErrorIs(t, failedErr, domain.ErrInvalidTransition,
			"if MarkExpired won, MarkPaymentFailed must return ErrInvalidTransition")
	}

	// Final state must be exactly one of the two terminal-equivalent
	// states; both lead to saga compensation downstream.
	var status string
	require.NoError(t, h.DB.QueryRow(`SELECT status FROM orders WHERE id=$1`, orderID).Scan(&status))
	assert.True(t, status == "expired" || status == "payment_failed",
		"final state must be expired or payment_failed; got %q", status)
}

// TestRace_FutureReservationNotEligible: a row whose reserved_until
// is in the FUTURE must NOT surface from FindExpiredReservations.
// D6 plan v4 §E case 3: future-reservation rows are filtered by the
// SQL predicate, full stop — no race window applies.
//
// This complements the unit test
// `TestSweep_HappyPath_ExpiresAndEmits` (which mocks the find query)
// by proving the actual SQL filter works on real data.
func TestRace_FutureReservationNotEligible(t *testing.T) {
	h, repo := d6RaceRepo(t)
	h.Reset(t)
	eventID := seedEventForOrder(t, h)

	// Seed a future-reservation row.
	_ = seedReservationWithFuture(t, h, eventID, 1)

	rows, err := repo.FindExpiredReservations(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Empty(t, rows, "future reservations must be filtered by the SQL predicate; no race window applies")
}
