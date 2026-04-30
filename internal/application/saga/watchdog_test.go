package saga_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/saga"
	"booking_monitor/internal/domain"
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

// recordingSagaMetrics is the test-side `saga.Metrics` implementation —
// captures every call so assertions can read the recorded counts
// instead of polling global Prometheus singletons. Lets tests run
// safely with `t.Parallel()` (CP1's parallel-counter race was
// reproducible specifically because counter assertions read shared
// global state) and makes assertion intent explicit at read time
// (`metrics.resolved["compensated"]` vs `testutil.ToFloat64(observability.X)`).
type recordingSagaMetrics struct {
	stuckFailedOrders int
	resolved          map[string]int
	findStuckErrors   int
	resolveDurations  []float64
	resolveAges       []float64
}

func newRecordingSagaMetrics() *recordingSagaMetrics {
	return &recordingSagaMetrics{resolved: make(map[string]int)}
}

func (m *recordingSagaMetrics) SetStuckFailedOrders(c int)         { m.stuckFailedOrders = c }
func (m *recordingSagaMetrics) IncResolved(outcome string)         { m.resolved[outcome]++ }
func (m *recordingSagaMetrics) IncFindStuckErrors()                { m.findStuckErrors++ }
func (m *recordingSagaMetrics) ObserveResolveDuration(s float64)   { m.resolveDurations = append(m.resolveDurations, s) }
func (m *recordingSagaMetrics) ObserveResolveAge(s float64)        { m.resolveAges = append(m.resolveAges, s) }

// watchdogHarness builds a Watchdog with config knobs shortened so
// tests run in milliseconds. Returns the mock repo so per-test setup
// can drive the scenario, and the recording metrics so assertions can
// read what the watchdog emitted.
func watchdogHarness(t *testing.T, comp application.SagaCompensator) (*saga.Watchdog, *mocks.MockOrderRepository, *recordingSagaMetrics) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	metrics := newRecordingSagaMetrics()

	cfg := saga.Config{
		WatchdogInterval: 50 * time.Millisecond,
		StuckThreshold:   1 * time.Millisecond,
		MaxFailedAge:     1 * time.Hour, // tested separately
		BatchSize:        10,
	}
	w := saga.NewWatchdog(repo, comp, cfg, metrics, mlog.NewNop())
	return w, repo, metrics
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
	t.Parallel()
	comp := &fakeCompensator{}
	w, repo, metrics := watchdogHarness(t, comp)

	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil)

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 0, comp.calls, "compensator MUST NOT be called when no stuck orders found")
	assert.Equal(t, 0, metrics.stuckFailedOrders,
		"gauge MUST be set to 0 on empty sweep — stale value would mislead operators")
	assert.Empty(t, metrics.resolved, "no resolutions on empty sweep")
}

// TestSweep_FindStuckError_ReturnsAndIncrements: when the
// FindStuckFailed query itself fails, the watchdog returns an error
// AND increments the dedicated error counter so dashboards can
// distinguish "no orders are stuck" from "watchdog itself is broken".
func TestSweep_FindStuckError_ReturnsAndIncrements(t *testing.T) {
	t.Parallel()
	comp := &fakeCompensator{}
	w, repo, metrics := watchdogHarness(t, comp)

	dbErr := errors.New("postgres: connection refused")
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, dbErr)

	err := w.Sweep(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr, "Sweep must wrap the DB error so callers can errors.Is it")

	assert.Equal(t, 1, metrics.findStuckErrors,
		"saga_watchdog_find_stuck_errors_total MUST increment on FindStuckFailed failure — operator's only signal that the watchdog is blind")
	assert.Equal(t, 0, comp.calls, "compensator MUST NOT be called when FindStuckFailed errored")
}

// TestSweep_Compensated_HappyPath: stuck order → GetByID returns
// Failed → compensator returns nil → outcome counter incremented +
// log line emitted. Pins the canonical resolution path.
func TestSweep_Compensated_HappyPath(t *testing.T) {
	t.Parallel()
	comp := &fakeCompensator{}
	w, repo, metrics := watchdogHarness(t, comp)

	stuckID := uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{{ID: stuckID, Age: 5 * time.Minute}}, nil)
	repo.EXPECT().GetByID(gomock.Any(), stuckID).Return(reconstructFailedOrder(t, stuckID), nil)

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 1, comp.calls, "compensator MUST be called on the stuck order")
	assert.NotEmpty(t, comp.payload, "compensator must receive a marshaled OrderFailedEvent payload")
	assert.Equal(t, 1, metrics.resolved["compensated"], "compensated outcome counter MUST increment")
	assert.Len(t, metrics.resolveDurations, 1, "resolve duration histogram observation MUST fire once")
	assert.Len(t, metrics.resolveAges, 1, "resolve age histogram observation MUST fire once")
}

// TestSweep_AlreadyCompensated_RaceWonByConsumer: between
// FindStuckFailed and GetByID, the saga consumer (regular path)
// completed compensation. Watchdog must skip + count "already_compensated"
// (benign race signal, distinct from real errors) AND must NOT call
// the compensator (would be a no-op anyway via internal idempotency,
// but counting separately gives operators a clean signal).
func TestSweep_AlreadyCompensated_RaceWonByConsumer(t *testing.T) {
	t.Parallel()
	comp := &fakeCompensator{}
	w, repo, metrics := watchdogHarness(t, comp)

	id := uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{{ID: id, Age: 5 * time.Minute}}, nil)
	// GetByID returns Compensated — saga consumer beat us
	compensated := domain.ReconstructOrder(id, 1, uuid.New(), 1, domain.OrderStatusCompensated, time.Now().Add(-2*time.Hour))
	repo.EXPECT().GetByID(gomock.Any(), id).Return(compensated, nil)

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 0, comp.calls,
		"compensator MUST NOT be called when GetByID shows the row is already Compensated — race won by consumer")
	assert.Equal(t, 1, metrics.resolved["already_compensated"],
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
	t.Parallel()
	comp := &fakeCompensator{}
	w, repo, metrics := watchdogHarness(t, comp)

	id := uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{{ID: id, Age: 2 * time.Hour}}, nil) // > MaxFailedAge=1h

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 0, comp.calls,
		"max-age branch MUST NOT call compensator — auto-transition without inventory verification is unsafe")
	assert.Equal(t, 1, metrics.resolved["max_age_exceeded"], "max_age_exceeded counter MUST increment")
}

// TestSweep_CompensatorError_RetriesNextSweep: compensator returns an
// error → outcome counter "compensator_error" increments, sweep
// continues to the next order (not aborted). Pins the per-order
// failure containment contract.
func TestSweep_CompensatorError_RetriesNextSweep(t *testing.T) {
	t.Parallel()
	comp := &fakeCompensator{err: errors.New("redis: revert blocked")}
	w, repo, metrics := watchdogHarness(t, comp)

	id1, id2 := uuid.New(), uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{
			{ID: id1, Age: 5 * time.Minute},
			{ID: id2, Age: 5 * time.Minute},
		}, nil)
	repo.EXPECT().GetByID(gomock.Any(), id1).Return(reconstructFailedOrder(t, id1), nil)
	repo.EXPECT().GetByID(gomock.Any(), id2).Return(reconstructFailedOrder(t, id2), nil)

	require.NoError(t, w.Sweep(context.Background()),
		"per-order compensator failures MUST NOT abort the whole sweep")
	assert.Equal(t, 2, comp.calls, "both stuck orders must be attempted, even though the first failed")
	assert.Equal(t, 2, metrics.resolved["compensator_error"], "compensator_error counter MUST increment per failed order")
}

// TestSweep_GetByIDError_DistinctLabel pins the post-review-fixup
// contract: a GetByID infrastructure failure increments the dedicated
// `getbyid_error` outcome label, NOT `compensator_error`. Operator
// runbooks branch on these labels — conflation would direct triage at
// Redis/compensator code when the real fault is the DB read.
func TestSweep_GetByIDError_DistinctLabel(t *testing.T) {
	t.Parallel()
	comp := &fakeCompensator{}
	w, repo, metrics := watchdogHarness(t, comp)

	id := uuid.New()
	repo.EXPECT().FindStuckFailed(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckFailed{{ID: id, Age: 5 * time.Minute}}, nil)
	repo.EXPECT().GetByID(gomock.Any(), id).Return(domain.Order{}, errors.New("postgres: read replica unreachable"))

	require.NoError(t, w.Sweep(context.Background()))

	assert.Equal(t, 0, comp.calls,
		"compensator MUST NOT be called when GetByID errored before reaching it")
	assert.Equal(t, 1, metrics.resolved["getbyid_error"], "getbyid_error MUST increment for DB read failures")
	assert.Equal(t, 0, metrics.resolved["compensator_error"], "compensator_error MUST NOT increment when failure is upstream of compensator")
}

// TestSweep_MixedOutcomes_PerOrderIsolation pins the per-order failure
// isolation contract across mixed outcomes in one sweep: a successful
// order's `compensated` increment must NOT be lost when a peer in the
// same batch fails. Most likely regression target if someone refactors
// the loop to bail on first per-order error.
func TestSweep_MixedOutcomes_PerOrderIsolation(t *testing.T) {
	t.Parallel()
	// Per-call compensator: order1 succeeds, order2 fails, order3 succeeds.
	togglingComp := &perCallCompensator{
		errs: []error{nil, errors.New("redis: blip on order 2"), nil},
	}
	w, repo, metrics := watchdogHarness(t, togglingComp)

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

	require.NoError(t, w.Sweep(context.Background()),
		"per-order failure must NOT propagate as a sweep-level error")
	assert.Equal(t, 3, togglingComp.calls, "all three orders must be attempted")
	assert.Equal(t, 2, metrics.resolved["compensated"], "two successful orders must each increment `compensated`")
	assert.Equal(t, 1, metrics.resolved["compensator_error"], "one failed order must increment `compensator_error`")
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
	t.Parallel()
	comp := &fakeCompensator{}
	w, repo, _ := watchdogHarness(t, comp)

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
	_ = id2 // gomock asserts GetByID(id2) is never called via the missing EXPECT.

	err := w.Sweep(ctx)
	assert.ErrorIs(t, err, context.Canceled, "ctx-cancelled sweep must surface context.Canceled")
	// Explicit assertion: only the FIRST order should have been
	// attempted. gomock's ctrl.Finish would surface a missing-EXPECT
	// for id2 at cleanup time with a cryptic message; this assertion
	// makes the loop-boundary contract obvious at the failure site.
	assert.Equal(t, 1, comp.calls,
		"loop must exit at the iteration boundary AFTER the first order, BEFORE the second")
}

// TestSweep_GaugeReflectsLastSweep: gauge is set to len(stuck) on
// every sweep — point-in-time, not cumulative. Two sweeps with
// different counts must show the latest, not the sum.
func TestSweep_GaugeReflectsLastSweep(t *testing.T) {
	t.Parallel()
	comp := &fakeCompensator{}
	w, repo, metrics := watchdogHarness(t, comp)

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
	assert.Equal(t, 3, metrics.stuckFailedOrders, "gauge after first sweep must reflect 3 stuck orders")

	require.NoError(t, w.Sweep(context.Background()))
	assert.Equal(t, 1, metrics.stuckFailedOrders,
		"gauge must reflect the LATEST sweep, not the sum — point-in-time semantics")
}
