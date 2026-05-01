//go:build integration

package pgintegration_test

import (
	"context"
	"errors"
	"testing"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	mlog "booking_monitor/internal/log"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for postgres.PostgresUnitOfWork against a real
// container.
//
// The UoW is the load-bearing primitive that the application's
// runUowThrough simulator (CP7's saga compensator tests) explicitly
// cannot verify. CP4b closes that gap: this suite exercises commit-
// on-nil, rollback-on-err, and the cross-aggregate atomicity that's
// the whole reason the multi-aggregate UoW exists.
//
// Load-bearing contracts pinned:
//
//   - fn returning nil → BOTH writes inside the closure persist.
//   - fn returning err → NO writes inside the closure persist (atomic
//     rollback). This is the contract the saga compensator and
//     OutboxRelay rely on for transactional consistency.
//   - The Repositories bundle is fresh per Do invocation — caching
//     would let a tx-bound repo leak to a concurrent caller (the
//     Morrison atomic-repositories foot-gun documented in uow.go).
//
// Out of scope for CP4b:
//   - Rollback-of-rollback-fails (when tx.Rollback itself returns a
//     non-ErrTxDone error and metrics.RecordRollbackFailure fires).
//     Reproducing this requires injecting a fault into the
//     `database/sql` driver — possible via a custom driver shim, but
//     out of scope here. The metric is exercised by the unit tests
//     in CP7's saga compensator suite (the recordingMetrics fake).

// uowHarness boots a container, builds the three concrete repos, and
// wires them into a PostgresUnitOfWork. The repos are returned for
// out-of-tx assertions (verifying the row state after commit /
// rollback).
func uowHarness(t *testing.T) (
	*pgintegration.Harness,
	application.UnitOfWork,
	domain.OrderRepository,
	domain.EventRepository,
	domain.OutboxRepository,
) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	orderRepo := postgres.NewPostgresOrderRepository(h.DB)
	eventRepo := postgres.NewPostgresEventRepository(h.DB)
	outboxRepo := postgres.NewPostgresOutboxRepository(h.DB)
	uow := postgres.NewPostgresUnitOfWork(
		h.DB, orderRepo, eventRepo, outboxRepo,
		mlog.NewNop(),
		application.NoopDBMetrics(),
	)
	return h, uow, orderRepo, eventRepo, outboxRepo
}

// TestUoW_CommitsOnNil: fn returns nil → all writes inside the
// closure persist. Out-of-tx GetByID confirms the writes are visible
// to the connection pool, not just the tx.
func TestUoW_CommitsOnNil(t *testing.T) {
	h, uow, _, eventRepo, outboxRepo := uowHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 100)
	outboxEv := newOutboxEvent(t, []byte(`{"committed":true}`))

	err := uow.Do(ctx, func(repos *application.Repositories) error {
		_, err := repos.Event.Create(ctx, ev)
		require.NoError(t, err)
		_, err = repos.Outbox.Create(ctx, outboxEv)
		require.NoError(t, err)
		return nil
	})
	require.NoError(t, err)

	// Both writes must be visible OUTSIDE the closure (post-commit).
	gotEvent, err := eventRepo.GetByID(ctx, ev.ID())
	require.NoError(t, err)
	assert.Equal(t, ev.ID(), gotEvent.ID(), "event row must persist after commit")

	pending, err := outboxRepo.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1, "outbox row must persist after commit")
	assert.Equal(t, outboxEv.ID(), pending[0].ID())
}

// TestUoW_RollsBackOnError: fn returning err → NO writes persist,
// even writes that succeeded before the error. Pins the
// transactional outbox + saga compensator atomicity guarantee.
//
// This is the test that closes the silent-failure-hunter Finding 3
// from CP7 — the application-layer simulator can't verify rollback
// because it doesn't run inside a real tx. This does.
func TestUoW_RollsBackOnError(t *testing.T) {
	h, uow, _, eventRepo, outboxRepo := uowHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newDomainEvent(t, 100)
	outboxEv := newOutboxEvent(t, []byte(`{"should_not_persist":true}`))

	sentinelErr := errors.New("simulated downstream failure")

	err := uow.Do(ctx, func(repos *application.Repositories) error {
		_, err := repos.Event.Create(ctx, ev)
		require.NoError(t, err, "Event.Create inside the closure succeeds (pre-rollback)")
		_, err = repos.Outbox.Create(ctx, outboxEv)
		require.NoError(t, err, "Outbox.Create inside the closure succeeds (pre-rollback)")
		// Now fail. The UoW MUST roll back BOTH inserts.
		return sentinelErr
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinelErr,
		"Do must propagate the fn error verbatim, not a rollback-wrapped variant")

	// Neither write must be visible — atomic rollback.
	_, err = eventRepo.GetByID(ctx, ev.ID())
	assert.ErrorIs(t, err, domain.ErrEventNotFound,
		"event row must NOT persist after rollback — atomicity is the load-bearing contract")

	pending, err := outboxRepo.ListPending(ctx, 100)
	require.NoError(t, err)
	assert.Empty(t, pending,
		"outbox row must NOT persist after rollback — same tx, same atomicity")
}

// TestUoW_FreshBundlePerInvocation: two concurrent Do calls must NOT
// share a Repositories bundle (which would leak a tx-bound repo to
// the other caller — the Morrison atomic-repositories foot-gun).
//
// We test this by running two Do calls in serial that each insert a
// distinct event. If the bundle were cached, the second call would
// see a tx pre-populated with the first call's writes via the
// bound repo. Our concrete check: each Do must succeed independently
// and both events must persist.
//
// (Concurrent goroutine-level testing of tx-bundle leakage requires
// driving two ongoing tx's from goroutines and asserting per-tx
// state visibility — covered by the Postgres tx isolation contract,
// which is the database's job to provide. This test pins that the
// UoW gives each Do its OWN bundle, not a shared one.)
func TestUoW_FreshBundlePerInvocation(t *testing.T) {
	h, uow, _, eventRepo, _ := uowHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev1 := newDomainEvent(t, 100)
	ev2 := newDomainEvent(t, 200)

	// Capture bundle pointers from each invocation. They MUST differ.
	var bundle1, bundle2 *application.Repositories
	require.NoError(t, uow.Do(ctx, func(repos *application.Repositories) error {
		bundle1 = repos
		_, err := repos.Event.Create(ctx, ev1)
		return err
	}))
	require.NoError(t, uow.Do(ctx, func(repos *application.Repositories) error {
		bundle2 = repos
		_, err := repos.Event.Create(ctx, ev2)
		return err
	}))

	require.NotNil(t, bundle1)
	require.NotNil(t, bundle2)
	assert.NotSame(t, bundle1, bundle2,
		"each Do invocation must build a fresh Repositories bundle — caching would leak a tx-bound repo to a concurrent caller (Morrison foot-gun, see uow.go)")

	// Both events persist.
	got1, err := eventRepo.GetByID(ctx, ev1.ID())
	require.NoError(t, err)
	assert.Equal(t, ev1.ID(), got1.ID())

	got2, err := eventRepo.GetByID(ctx, ev2.ID())
	require.NoError(t, err)
	assert.Equal(t, ev2.ID(), got2.ID())
}

// TestUoW_CrossAggregateAtomicity: the saga compensator's "pattern"
// — one Do call that mutates orders + events_outbox together. If
// either fails, neither persists.
//
// This test pins the EXACT shape used in production by:
//   - SagaCompensator (rollbacks orders + creates compensation outbox)
//   - Reconciler force-fail (transitions orders + creates fail outbox)
//   - OrderMessageProcessor (creates order + outbox in one tx)
//
// A regression that broke cross-aggregate atomicity would manifest
// as a partially-rolled-back state — orders flipped status without
// the outbox event firing, or vice versa.
func TestUoW_CrossAggregateAtomicity(t *testing.T) {
	h, uow, orderRepo, _, outboxRepo := uowHarness(t)
	h.Reset(t)

	ctx := context.Background()

	// Pre-seed an event so order's FK has a target.
	eventID := uuid.New()
	h.SeedEvent(t, eventID.String(), "Test", 100)

	orderID, err := uuid.NewV7()
	require.NoError(t, err)
	order, err := domain.NewOrder(orderID, 1, eventID, 1)
	require.NoError(t, err)

	outboxEv := newOutboxEvent(t, []byte(`{"order_id":"`+orderID.String()+`"}`))
	sentinelErr := errors.New("simulated mid-tx failure")

	err = uow.Do(ctx, func(repos *application.Repositories) error {
		_, err := repos.Order.Create(ctx, order)
		require.NoError(t, err)
		_, err = repos.Outbox.Create(ctx, outboxEv)
		require.NoError(t, err)
		return sentinelErr
	})
	require.ErrorIs(t, err, sentinelErr)

	// CRITICAL: BOTH the order row AND the outbox row must be absent.
	// A regression that committed one but not the other would
	// silently corrupt the saga + outbox guarantees.
	_, err = orderRepo.GetByID(ctx, order.ID())
	assert.ErrorIs(t, err, domain.ErrOrderNotFound,
		"order row MUST NOT persist when sibling outbox write was rolled back")

	pending, err := outboxRepo.ListPending(ctx, 100)
	require.NoError(t, err)
	assert.Empty(t, pending,
		"outbox row MUST NOT persist when sibling order write was rolled back — cross-aggregate atomicity is the saga's load-bearing guarantee")
}

// TestUoW_BeginTxError_Surfaces: if BeginTx fails (broken DB conn,
// ctx cancelled before begin), Do MUST surface the error and never
// invoke fn. We exercise this by passing a cancelled context.
func TestUoW_BeginTxError_Surfaces(t *testing.T) {
	h, uow, _, _, _ := uowHarness(t)
	h.Reset(t)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE Do is invoked

	var fnCalled bool
	err := uow.Do(cancelledCtx, func(_ *application.Repositories) error {
		fnCalled = true
		return nil
	})
	require.Error(t, err)
	assert.False(t, fnCalled,
		"fn MUST NOT be invoked when BeginTx fails — the closure has no tx to run against")
	assert.Contains(t, err.Error(), "begin tx",
		"BeginTx error must be wrapped with the begin-tx context for triage")
}
