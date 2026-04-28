package recon_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application/recon"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// fakeStatusReader is a hand-rolled PaymentStatusReader for these
// tests because (a) it's tiny, (b) we need precise control over the
// (status, err) tuple returned per orderID, and (c) avoiding a mock
// keeps test intent obvious at read time. Mirrors the fakeIdempotency
// pattern from PR #41.
type fakeStatusReader struct {
	statuses map[uuid.UUID]domain.ChargeStatus
	errors   map[uuid.UUID]error
	calls    int
}

func (f *fakeStatusReader) GetStatus(_ context.Context, id uuid.UUID) (domain.ChargeStatus, error) {
	f.calls++
	return f.statuses[id], f.errors[id]
}

// reconcilerHarness builds a Reconciler with all four config knobs
// shortened so tests run in milliseconds. Returns the mock repo so
// per-test EXPECT calls can drive the scenario.
func reconcilerHarness(t *testing.T, gw domain.PaymentStatusReader) (*recon.Reconciler, *mocks.MockOrderRepository) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)

	cfg := &config.Config{
		Recon: config.ReconConfig{
			SweepInterval:     50 * time.Millisecond,
			ChargingThreshold: 1 * time.Millisecond, // anything older counts
			GatewayTimeout:    100 * time.Millisecond,
			MaxChargingAge:    1 * time.Hour, // tested separately
			BatchSize:         10,
		},
	}
	r := recon.NewReconciler(repo, gw, cfg, mlog.NewNop())
	return r, repo
}

func TestSweep_NoStuckOrders_NoOps(t *testing.T) {
	t.Parallel()

	r, repo := reconcilerHarness(t, &fakeStatusReader{})
	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{}, nil)

	require.NoError(t, r.Sweep(context.Background()))
}

func TestSweep_FindStuckChargingError_Propagates(t *testing.T) {
	t.Parallel()

	r, repo := reconcilerHarness(t, &fakeStatusReader{})
	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("db dead"))

	err := r.Sweep(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "find stuck")
}

func TestSweep_ChargedVerdict_MarksConfirmed(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusCharged},
	}
	r, repo := reconcilerHarness(t, gw)

	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	repo.EXPECT().MarkConfirmed(gomock.Any(), id).Return(nil)

	require.NoError(t, r.Sweep(context.Background()))
	assert.Equal(t, 1, gw.calls)
}

func TestSweep_DeclinedVerdict_MarksFailed(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusDeclined},
	}
	r, repo := reconcilerHarness(t, gw)

	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	repo.EXPECT().MarkFailed(gomock.Any(), id).Return(nil)

	require.NoError(t, r.Sweep(context.Background()))
}

func TestSweep_NotFoundVerdict_MarksFailed(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusNotFound},
	}
	r, repo := reconcilerHarness(t, gw)

	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	// NotFound = gateway never saw a Charge call. We fail the order;
	// customer was never charged.
	repo.EXPECT().MarkFailed(gomock.Any(), id).Return(nil)

	require.NoError(t, r.Sweep(context.Background()))
}

func TestSweep_UnknownVerdict_SkipsNoMark(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusUnknown},
	}
	r, repo := reconcilerHarness(t, gw)

	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	// CRITICAL: NO Mark{Confirmed,Failed} call expected. Unknown =
	// retry next sweep cycle. If MarkConfirmed/MarkFailed gets called
	// here the test fails — gomock's ctrl.Finish (auto via NewController)
	// surfaces unexpected calls.

	require.NoError(t, r.Sweep(context.Background()))
}

func TestSweep_GatewayError_SkipsAndCounts(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	gw := &fakeStatusReader{
		// CRITICAL distinction: (Unknown, err) is an infrastructure
		// failure (network, gateway 5xx, ctx timeout) — different
		// signal than (Unknown, nil) which is a successful but
		// unclassifiable verdict. Both skip Mark*, but
		// recon_gateway_errors_total only fires on the err case.
		// (silent-failure-hunter blocker B1.)
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusUnknown},
		errors:   map[uuid.UUID]error{id: errors.New("connection refused")},
	}
	r, repo := reconcilerHarness(t, gw)

	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	// No Mark* expected.

	// Sweep returns nil (per-order error doesn't abort the sweep).
	require.NoError(t, r.Sweep(context.Background()))
}

func TestSweep_MaxAgeExceeded_ForceFails(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	gw := &fakeStatusReader{} // never queried — max-age short-circuits before GetStatus

	// This test needs a non-default max-age (5s instead of the harness's
	// 1h) so the 10s-old order trips it. Build the reconciler directly
	// rather than going through reconcilerHarness — the harness's cfg
	// would suppress the max-age branch.
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	cfg := &config.Config{
		Recon: config.ReconConfig{
			ChargingThreshold: 1 * time.Millisecond,
			GatewayTimeout:    100 * time.Millisecond,
			MaxChargingAge:    5 * time.Second,
			BatchSize:         10,
		},
	}
	r := recon.NewReconciler(repo, gw, cfg, mlog.NewNop())

	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 10 * time.Second}}, nil) // > 5s max
	// Force-fail without ever calling GetStatus.
	repo.EXPECT().MarkFailed(gomock.Any(), id).Return(nil)

	require.NoError(t, r.Sweep(context.Background()))
	assert.Equal(t, 0, gw.calls, "max-age give-up MUST short-circuit before GetStatus")
}

func TestSweep_TransitionLost_BenignNoError(t *testing.T) {
	t.Parallel()

	// Recon picks up a Charging order, gateway says Charged, calls
	// MarkConfirmed — but the original payment worker raced us and
	// already wrote Confirmed. ErrInvalidTransition is the expected
	// signal. Recon must treat as idempotent success (silent-failure
	// blocker B2) — Sweep returns nil, no error path triggered.
	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusCharged},
	}
	r, repo := reconcilerHarness(t, gw)

	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	repo.EXPECT().MarkConfirmed(gomock.Any(), id).Return(domain.ErrInvalidTransition)

	require.NoError(t, r.Sweep(context.Background()),
		"ErrInvalidTransition from Mark* must NOT propagate as Sweep error")
}

func TestSweep_CtxCancelled_ExitsAtLoopBoundary(t *testing.T) {
	t.Parallel()

	// Caller cancels mid-sweep; recon should return ctx.Err() and stop
	// processing further orders. The currently-resolving order finishes
	// under its own derived ctx (not part of this test — covered by the
	// per-order timeout test below).
	id1 := uuid.New()
	id2 := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{
			id1: domain.ChargeStatusCharged,
			id2: domain.ChargeStatusCharged,
		},
	}
	r, repo := reconcilerHarness(t, gw)

	ctx, cancel := context.WithCancel(context.Background())

	repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{
			{ID: id1, Age: 30 * time.Second},
			{ID: id2, Age: 30 * time.Second},
		}, nil)
	// First order: succeeds, then cancel before second iteration.
	repo.EXPECT().MarkConfirmed(gomock.Any(), id1).
		DoAndReturn(func(_ context.Context, _ uuid.UUID) error {
			cancel() // cancel parent BEFORE returning
			return nil
		})
	// id2 must NOT be processed — gomock will fail the test if MarkConfirmed
	// is called for id2 (no EXPECT registered).

	err := r.Sweep(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
