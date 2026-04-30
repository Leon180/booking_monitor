package saga_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/saga"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// fakeCompensator is a hand-rolled SagaCompensator. The interface is
// a single method, so a mock would be more ceremony than the
// hand-rolled stub — and per-test setup stays obvious at read time.
// Mirrors the fakeStatusReader pattern in the recon test file.
type fakeCompensator struct {
	err     error
	calls   int
	payload []byte
}

func (f *fakeCompensator) HandleOrderFailed(_ context.Context, payload []byte) error {
	f.calls++
	f.payload = payload
	return f.err
}

// watchdogHarness builds a Watchdog with config knobs shortened so
// tests run in milliseconds. Returns the mock repo so per-test setup
// can drive the scenario. Accepts any SagaCompensator implementation
// — `fakeCompensator` for single-outcome tests, `perCallCompensator`
// for mixed-outcome sweeps.
func watchdogHarness(t *testing.T, comp application.SagaCompensator) (*saga.Watchdog, *mocks.MockOrderRepository) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)

	cfg := &config.Config{
		Saga: config.SagaConfig{
			WatchdogInterval: 50 * time.Millisecond,
			StuckThreshold:   1 * time.Millisecond,
			MaxFailedAge:     1 * time.Hour, // tested separately
			BatchSize:        10,
		},
	}
	w := saga.NewWatchdog(repo, comp, cfg, mlog.NewNop())
	return w, repo
}

// reconstructFailedOrder is a helper for tests that need a domain.Order
// in the Failed state to drive the watchdog's GetByID branch.
func reconstructFailedOrder(t *testing.T, id uuid.UUID) domain.Order {
	t.Helper()
	return domain.ReconstructOrder(
		id, 1, uuid.New(), 1, domain.OrderStatusFailed, time.Now().Add(-2*time.Hour),
	)
}

// TestSweep_NoStuckOrders_NoOps: empty FindStuckFailed result → gauge
// updated to 0, compensator never called, no resolve histograms
// observed.
func TestSweep_NoStuckOrders_NoOps(t *testing.T) {
	comp := &fakeCompensator{}
	w, repo := watchdogHarness(t, comp)

	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 0, comp.calls, "compensator MUST NOT be called when no stuck orders found")
	assert.Equal(t, float64(0), testutil.ToFloat64(observability.SagaStuckFailedOrders),
		"gauge MUST be set to 0 on empty sweep — stale value would mislead operators")
}

// TestSweep_FindStuckError_ReturnsAndIncrements: when the
// FindStuckFailed query itself fails, the watchdog returns an error
// AND increments the dedicated error counter so dashboards can
// distinguish "no orders are stuck" from "watchdog itself is broken".
func TestSweep_FindStuckError_ReturnsAndIncrements(t *testing.T) {
	comp := &fakeCompensator{}
	w, repo := watchdogHarness(t, comp)

	dbErr := errors.New("postgres: connection refused")
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, dbErr)

	before := testutil.ToFloat64(observability.SagaWatchdogFindStuckErrorsTotal)

	err := w.Sweep(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr, "Sweep must wrap the DB error so callers can errors.Is it")

	after := testutil.ToFloat64(observability.SagaWatchdogFindStuckErrorsTotal)
	assert.Equal(t, before+1, after,
		"saga_watchdog_find_stuck_errors_total MUST increment on FindStuckFailed failure — operator's only signal that the watchdog is blind")
	assert.Equal(t, 0, comp.calls, "compensator MUST NOT be called when FindStuckFailed errored")
}

// TestSweep_Compensated_HappyPath: stuck order → GetByID returns
// Failed → compensator returns nil → outcome counter incremented +
// log line emitted. Pins the canonical resolution path.
func TestSweep_Compensated_HappyPath(t *testing.T) {
	comp := &fakeCompensator{}
	w, repo := watchdogHarness(t, comp)

	stuckID := uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{{ID: stuckID, Age: 5 * time.Minute}}, nil)
	repo.EXPECT().GetByID(gomock.Any(), stuckID).Return(reconstructFailedOrder(t, stuckID), nil)

	before := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensated"))

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 1, comp.calls, "compensator MUST be called on the stuck order")
	assert.NotEmpty(t, comp.payload, "compensator must receive a marshaled OrderFailedEvent payload")

	after := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensated"))
	assert.Equal(t, before+1, after, "compensated outcome counter MUST increment")
}

// TestSweep_AlreadyCompensated_RaceWonByConsumer: between
// FindStuckFailed and GetByID, the saga consumer (regular path)
// completed compensation. Watchdog must skip + count "already_compensated"
// (benign race signal, distinct from real errors) AND must NOT call
// the compensator (would be a no-op anyway via internal idempotency,
// but counting separately gives operators a clean signal).
func TestSweep_AlreadyCompensated_RaceWonByConsumer(t *testing.T) {
	comp := &fakeCompensator{}
	w, repo := watchdogHarness(t, comp)

	id := uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{{ID: id, Age: 5 * time.Minute}}, nil)
	// GetByID returns Compensated — saga consumer beat us
	compensated := domain.ReconstructOrder(id, 1, uuid.New(), 1, domain.OrderStatusCompensated, time.Now().Add(-2*time.Hour))
	repo.EXPECT().GetByID(gomock.Any(), id).Return(compensated, nil)

	before := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("already_compensated"))

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 0, comp.calls,
		"compensator MUST NOT be called when GetByID shows the row is already Compensated — race won by consumer")
	after := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("already_compensated"))
	assert.Equal(t, before+1, after,
		"already_compensated counter MUST increment — distinct from compensated for race-vs-success operator signal")
}

// TestSweep_MaxAgeExceeded_DoesNotForceTransition: when an order is
// older than MaxFailedAge, the watchdog logs ERROR + emits the
// max_age_exceeded counter but MUST NOT call the compensator and MUST
// NOT auto-transition the row. Forcing Failed → Compensated without
// verifying inventory was reverted is unsafe.
//
// Use the harness's default MaxFailedAge=1h and feed an Age=2h to
// trip the branch. Avoids needing a test-only mutator on Watchdog.
func TestSweep_MaxAgeExceeded_DoesNotForceTransition(t *testing.T) {
	comp := &fakeCompensator{}
	w, repo := watchdogHarness(t, comp)

	id := uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{{ID: id, Age: 2 * time.Hour}}, nil) // > MaxFailedAge=1h

	before := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("max_age_exceeded"))

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 0, comp.calls,
		"max-age branch MUST NOT call compensator — auto-transition without inventory verification is unsafe")
	after := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("max_age_exceeded"))
	assert.Equal(t, before+1, after, "max_age_exceeded counter MUST increment")
}

// TestSweep_CompensatorError_RetriesNextSweep: compensator returns an
// error → outcome counter "compensator_error" increments, sweep
// continues to the next order (not aborted). Pins the per-order
// failure containment contract.
func TestSweep_CompensatorError_RetriesNextSweep(t *testing.T) {
	comp := &fakeCompensator{err: errors.New("redis: revert blocked")}
	w, repo := watchdogHarness(t, comp)

	id1, id2 := uuid.New(), uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{
			{ID: id1, Age: 5 * time.Minute},
			{ID: id2, Age: 5 * time.Minute},
		}, nil)
	repo.EXPECT().GetByID(gomock.Any(), id1).Return(reconstructFailedOrder(t, id1), nil)
	repo.EXPECT().GetByID(gomock.Any(), id2).Return(reconstructFailedOrder(t, id2), nil)

	before := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensator_error"))

	require.NoError(t, w.Sweep(context.Background()),
		"per-order compensator failures MUST NOT abort the whole sweep")
	assert.Equal(t, 2, comp.calls, "both stuck orders must be attempted, even though the first failed")

	after := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensator_error"))
	assert.Equal(t, before+2, after, "compensator_error counter MUST increment per failed order")
}

// TestSweep_GetByIDError_DistinctLabel pins the post-review-fixup
// contract: a GetByID infrastructure failure increments the dedicated
// `getbyid_error` outcome label, NOT `compensator_error`. Operator
// runbooks branch on these labels — conflation would direct triage at
// Redis/compensator code when the real fault is the DB read.
func TestSweep_GetByIDError_DistinctLabel(t *testing.T) {
	comp := &fakeCompensator{}
	w, repo := watchdogHarness(t, comp)

	id := uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{{ID: id, Age: 5 * time.Minute}}, nil)
	repo.EXPECT().GetByID(gomock.Any(), id).Return(domain.Order{}, errors.New("postgres: read replica unreachable"))

	beforeGet := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("getbyid_error"))
	beforeComp := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensator_error"))

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 0, comp.calls,
		"compensator MUST NOT be called when GetByID errored before reaching it")

	afterGet := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("getbyid_error"))
	afterComp := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensator_error"))
	assert.Equal(t, beforeGet+1, afterGet, "getbyid_error MUST increment for DB read failures")
	assert.Equal(t, beforeComp, afterComp, "compensator_error MUST NOT increment when failure is upstream of compensator")
}

// TestSweep_MixedOutcomes_PerOrderIsolation pins the per-order failure
// isolation contract across mixed outcomes in one sweep: a successful
// order's `compensated` increment must NOT be lost when a peer in the
// same batch fails. Most likely regression target if someone refactors
// the loop to bail on first per-order error.
func TestSweep_MixedOutcomes_PerOrderIsolation(t *testing.T) {
	// Per-call compensator: order1 succeeds, order2 fails, order3 succeeds.
	togglingComp := &perCallCompensator{
		errs: []error{nil, errors.New("redis: blip on order 2"), nil},
	}
	w, repo := watchdogHarness(t, togglingComp)

	id1, id2, id3 := uuid.New(), uuid.New(), uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{
			{ID: id1, Age: 5 * time.Minute},
			{ID: id2, Age: 5 * time.Minute},
			{ID: id3, Age: 5 * time.Minute},
		}, nil)
	repo.EXPECT().GetByID(gomock.Any(), id1).Return(reconstructFailedOrder(t, id1), nil)
	repo.EXPECT().GetByID(gomock.Any(), id2).Return(reconstructFailedOrder(t, id2), nil)
	repo.EXPECT().GetByID(gomock.Any(), id3).Return(reconstructFailedOrder(t, id3), nil)

	beforeOK := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensated"))
	beforeErr := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensator_error"))

	require.NoError(t, w.Sweep(context.Background()),
		"per-order failure must NOT propagate as a sweep-level error")
	assert.Equal(t, 3, togglingComp.calls, "all three orders must be attempted")

	afterOK := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensated"))
	afterErr := testutil.ToFloat64(observability.SagaWatchdogResolvedTotal.WithLabelValues("compensator_error"))
	assert.Equal(t, beforeOK+2, afterOK, "two successful orders must each increment `compensated`")
	assert.Equal(t, beforeErr+1, afterErr, "one failed order must increment `compensator_error`")
}

// perCallCompensator is a stub that returns errs[i] for the i-th call.
// Used only by TestSweep_MixedOutcomes_PerOrderIsolation.
type perCallCompensator struct {
	errs  []error
	calls int
}

func (p *perCallCompensator) HandleOrderFailed(_ context.Context, _ []byte) error {
	idx := p.calls
	p.calls++
	if idx < len(p.errs) {
		return p.errs[idx]
	}
	return nil
}

// TestSweep_ContextCancelled_ExitsCleanly: parent ctx cancelled
// mid-sweep → loop exits at the next iteration boundary. Tests SIGTERM
// graceful-shutdown semantics.
func TestSweep_ContextCancelled_ExitsCleanly(t *testing.T) {
	comp := &fakeCompensator{}
	w, repo := watchdogHarness(t, comp)

	// Two stuck orders. Cancel the ctx after the first GetByID; the
	// loop must exit at the iteration boundary BEFORE the second is
	// processed.
	id1, id2 := uuid.New(), uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{
			{ID: id1, Age: 5 * time.Minute},
			{ID: id2, Age: 5 * time.Minute},
		}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	repo.EXPECT().GetByID(gomock.Any(), id1).
		DoAndReturn(func(_ context.Context, _ uuid.UUID) (domain.Order, error) {
			cancel() // cancel after the first GetByID succeeds
			return reconstructFailedOrder(t, id1), nil
		})
	// id2 EXPECTATIONS DELIBERATELY OMITTED: gomock fails the test if
	// they fire, asserting the loop exited at the boundary.

	err := w.Sweep(ctx)
	assert.ErrorIs(t, err, context.Canceled, "ctx-cancelled sweep must surface context.Canceled")
}

// TestSweep_GaugeReflectsLastSweep: gauge is set to len(stuck) on
// every sweep — point-in-time, not cumulative. Two sweeps with
// different counts must show the latest, not the sum.
func TestSweep_GaugeReflectsLastSweep(t *testing.T) {
	comp := &fakeCompensator{}
	w, repo := watchdogHarness(t, comp)

	// First sweep: 3 stuck orders
	id1, id2, id3 := uuid.New(), uuid.New(), uuid.New()
	gomock.InOrder(
		repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
			Return([]domain.StuckFailed{
				{ID: id1}, {ID: id2}, {ID: id3},
			}, nil),
		// Second sweep: 1 stuck order
		repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
			Return([]domain.StuckFailed{{ID: id1}}, nil),
	)
	for _, id := range []uuid.UUID{id1, id2, id3, id1} {
		repo.EXPECT().GetByID(gomock.Any(), id).Return(reconstructFailedOrder(t, id), nil)
	}

	require.NoError(t, w.Sweep(context.Background()))
	assert.Equal(t, float64(3), testutil.ToFloat64(observability.SagaStuckFailedOrders))

	require.NoError(t, w.Sweep(context.Background()))
	assert.Equal(t, float64(1), testutil.ToFloat64(observability.SagaStuckFailedOrders),
		"gauge must reflect the LATEST sweep, not the sum — point-in-time semantics")
}
