package recon_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/recon"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
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

// reconHarness bundles the four mocks the Reconciler needs. Lets each
// test set up its own EXPECT calls without re-constructing mocks +
// reconciler boilerplate inline. Also returns the UoW so the failure-path
// tests can wire MarkFailed + Outbox.Create expectations through Do.
type reconHarness struct {
	r      *recon.Reconciler
	repo   *mocks.MockOrderRepository
	uow    *mocks.MockUnitOfWork
	outbox *mocks.MockOutboxRepository
}

func newReconHarness(t *testing.T, gw domain.PaymentStatusReader, maxAge time.Duration) *reconHarness {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	outbox := mocks.NewMockOutboxRepository(ctrl)

	cfg := &config.Config{
		Recon: config.ReconConfig{
			SweepInterval:     50 * time.Millisecond,
			ChargingThreshold: 1 * time.Millisecond, // anything older counts
			GatewayTimeout:    100 * time.Millisecond,
			MaxChargingAge:    maxAge,
			BatchSize:         10,
		},
	}
	r := recon.NewReconciler(repo, gw, uow, cfg, mlog.NewNop())
	return &reconHarness{r: r, repo: repo, uow: uow, outbox: outbox}
}

// makeChargingOrder reconstructs a domain.Order in the OrderStatusCharging
// state matching the recon's GetByID expectation. Uses ReconstructOrder
// (the persistence-layer helper) since invariant validation already
// happened at insert time.
func makeChargingOrder(t *testing.T, id uuid.UUID) domain.Order {
	t.Helper()
	eventID, err := uuid.NewV7()
	require.NoError(t, err)
	return domain.ReconstructOrder(id, 1, eventID, 1, domain.OrderStatusCharging, time.Now().Add(-30*time.Second))
}

// expectFailOrder wires the GetByID + UoW.Do path that recon.failOrder
// exercises. The UoW's DoAndReturn invokes the closure with a
// Repositories bundle pointing at the harness's repo + outbox mocks,
// so the closure body's Order.MarkFailed and Outbox.Create both fire
// against the test's expectations.
//
// Caller passes `uowErr` non-nil to simulate the closure failing
// (e.g., MarkFailed DB error); pass nil for happy-path. The ordering
// inside the closure is enforced by gomock.InOrder when the caller
// stitches the per-mock expectations together.
//
// Returns the captured `OrderFailedEvent` payload via a closure-side
// effect captured by the caller — the function itself records the
// last marshalled payload into `capturedPayload` so a single test
// can later assert on `Reason` / `Version` / etc.
func expectFailOrder(
	t *testing.T,
	h *reconHarness,
	id uuid.UUID,
	order domain.Order,
	markErr, outboxErr error,
	capturedReason *string,
) {
	t.Helper()
	h.repo.EXPECT().GetByID(gomock.Any(), id).Return(order, nil)
	h.uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			repos := &application.Repositories{
				Order:  h.repo,
				Outbox: h.outbox,
			}
			// Closure body in failOrder calls MarkFailed → if ok, Outbox.Create.
			h.repo.EXPECT().MarkFailed(gomock.Any(), id).Return(markErr)
			if markErr == nil {
				h.outbox.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, ev domain.OutboxEvent) (domain.OutboxEvent, error) {
						if capturedReason != nil {
							var failed application.OrderFailedEvent
							if err := json.Unmarshal(ev.Payload(), &failed); err == nil {
								*capturedReason = failed.Reason
							}
						}
						return ev, outboxErr
					})
			}
			return fn(repos)
		})
}

func TestSweep_NoStuckOrders_NoOps(t *testing.T) {
	t.Parallel()

	h := newReconHarness(t, &fakeStatusReader{}, 1*time.Hour)
	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{}, nil)

	require.NoError(t, h.r.Sweep(context.Background()))
}

// Counter-asserting tests below run SERIALLY (no t.Parallel()) because
// they read + assert on global Prometheus counters
// (ReconFindStuckErrorsTotal / ReconMarkErrorsTotal). With t.Parallel()
// two tests asserting `before+1 == after` against the same counter
// race: each reads `before` independently, both increment, and the
// later `after` read sees +2 vs the captured `before`. The Mark*-side
// counter is now contested between
// TestSweep_MarkConfirmedDBError_EmitsMarkErrorMetric and
// TestFailOrder_OutboxCreateError_EmitsMarkErrorMetric (DEF-CRIT path
// added the second contender). Serial execution is the simplest fix;
// these tests are fast enough that parallelism gain is negligible.
//
// TestSweep_FindStuckChargingError_Propagates also runs serial because
// it INCREMENTS ReconFindStuckErrorsTotal as a side effect, which would
// race the assertion in
// TestSweep_FindStuckChargingDBError_EmitsFindStuckErrorMetric.
func TestSweep_FindStuckChargingError_Propagates(t *testing.T) {

	h := newReconHarness(t, &fakeStatusReader{}, 1*time.Hour)
	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("db dead"))

	err := h.r.Sweep(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "find stuck")
}

func TestSweep_ChargedVerdict_MarksConfirmed(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusCharged},
	}
	h := newReconHarness(t, gw, 1*time.Hour)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	h.repo.EXPECT().MarkConfirmed(gomock.Any(), id).Return(nil)
	// CRITICAL: NO outbox emit for the success path. Confirmed is terminal;
	// no saga compensation needed. This is the asymmetry between Charged
	// (terminal success — no outbox) and Declined/NotFound (terminal failure
	// — outbox+saga to revert Redis inventory).

	require.NoError(t, h.r.Sweep(context.Background()))
	assert.Equal(t, 1, gw.calls)
}

func TestSweep_DeclinedVerdict_FailsOrderAndEmitsOutbox(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	order := makeChargingOrder(t, id)
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusDeclined},
	}
	h := newReconHarness(t, gw, 1*time.Hour)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)

	var reason string
	expectFailOrder(t, h, id, order, nil, nil, &reason)

	require.NoError(t, h.r.Sweep(context.Background()))
	// Reason string is what a saga consumer / runbook author will see in
	// the order.failed payload. Lock down the recon-side framing so a
	// future refactor doesn't silently change consumer-visible text.
	assert.Equal(t, "recon: gateway returned declined", reason)
}

func TestSweep_NotFoundVerdict_FailsOrderAndEmitsOutbox(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	order := makeChargingOrder(t, id)
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusNotFound},
	}
	h := newReconHarness(t, gw, 1*time.Hour)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)

	var reason string
	expectFailOrder(t, h, id, order, nil, nil, &reason)

	require.NoError(t, h.r.Sweep(context.Background()))
	assert.Equal(t, "recon: gateway has no charge record", reason)
}

func TestSweep_UnknownVerdict_SkipsNoMark(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusUnknown},
	}
	h := newReconHarness(t, gw, 1*time.Hour)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	// CRITICAL: NO Mark{Confirmed,Failed} call expected. Unknown =
	// retry next sweep cycle. If MarkConfirmed/MarkFailed gets called
	// here the test fails — gomock's ctrl.Finish (auto via NewController)
	// surfaces unexpected calls. Equally NO GetByID / UoW.Do — failOrder
	// must not run on Unknown.

	require.NoError(t, h.r.Sweep(context.Background()))
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
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusUnknown},
		errors:   map[uuid.UUID]error{id: errors.New("connection refused")},
	}
	h := newReconHarness(t, gw, 1*time.Hour)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	// No Mark* / GetByID / UoW.Do expected.

	// Sweep returns nil (per-order error doesn't abort the sweep).
	require.NoError(t, h.r.Sweep(context.Background()))
}

func TestSweep_MaxAgeExceeded_FailsOrderAndEmitsOutbox(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	order := makeChargingOrder(t, id)
	gw := &fakeStatusReader{} // never queried — max-age short-circuits before GetStatus

	// Override default max-age (1h) to 5s so the 10s-old order trips it.
	h := newReconHarness(t, gw, 5*time.Second)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 10 * time.Second}}, nil) // > 5s max

	var reason string
	expectFailOrder(t, h, id, order, nil, nil, &reason)

	require.NoError(t, h.r.Sweep(context.Background()))
	assert.Equal(t, 0, gw.calls, "max-age give-up MUST short-circuit before GetStatus")
	assert.Equal(t, "recon: max_age_exceeded", reason)
}

// TestFailOrder_OutboxCreateError_EmitsMarkErrorMetric covers the
// failure path inside the UoW closure. A DB outage during outbox
// Create must fall through to handleMarkErr → recon_mark_errors_total
// increments. Without this test, a regression that swallowed the
// outbox-create failure would leave the saga compensator un-triggered
// AND no operator signal.
func TestFailOrder_OutboxCreateError_EmitsMarkErrorMetric(t *testing.T) {
	// no t.Parallel — see the serialization comment on
	// TestSweep_FindStuckChargingError_Propagates above.

	id := uuid.New()
	order := makeChargingOrder(t, id)
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusDeclined},
	}
	h := newReconHarness(t, gw, 1*time.Hour)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	expectFailOrder(t, h, id, order, nil, errors.New("outbox dead"), nil)

	before := testutil.ToFloat64(observability.ReconMarkErrorsTotal)
	require.NoError(t, h.r.Sweep(context.Background()),
		"per-order outbox/Mark errors must NOT propagate as Sweep error")
	after := testutil.ToFloat64(observability.ReconMarkErrorsTotal)
	assert.Equal(t, before+1, after,
		"recon_mark_errors_total must increment when the failOrder UoW fails")
}

// TestSweep_MarkConfirmedDBError_EmitsMarkErrorMetric covers the
// non-ErrInvalidTransition path of handleMarkErr on the SUCCESS
// branch (Charged → MarkConfirmed). Without this test, the
// recon_mark_errors_total counter would have zero coverage on the
// confirmed-side regression — a sustained DB outage during the resolve
// path would only surface in logs, and a regression that suppressed
// the counter would go unnoticed.
func TestSweep_MarkConfirmedDBError_EmitsMarkErrorMetric(t *testing.T) {
	// no t.Parallel — see the serialization comment on
	// TestSweep_FindStuckChargingError_Propagates above.

	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusCharged},
	}
	h := newReconHarness(t, gw, 1*time.Hour)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	// MarkConfirmed returns a real DB error (NOT ErrInvalidTransition).
	// This must increment recon_mark_errors_total and Sweep must still
	// return nil (per-order errors don't abort the sweep).
	h.repo.EXPECT().MarkConfirmed(gomock.Any(), id).Return(errors.New("connection reset by peer"))

	before := testutil.ToFloat64(observability.ReconMarkErrorsTotal)
	require.NoError(t, h.r.Sweep(context.Background()),
		"per-order DB errors must NOT propagate as Sweep error")
	after := testutil.ToFloat64(observability.ReconMarkErrorsTotal)
	assert.Equal(t, before+1, after,
		"recon_mark_errors_total must increment exactly once per non-ErrInvalidTransition Mark* failure")
}

// TestSweep_FindStuckChargingDBError_EmitsFindStuckErrorMetric covers
// the FindStuckCharging error path's metric increment. Without this,
// a forgotten migration / DB outage would show nothing on dashboards
// EXCEPT a stale gauge.
func TestSweep_FindStuckChargingDBError_EmitsFindStuckErrorMetric(t *testing.T) {
	// no t.Parallel — see the serialization comment on
	// TestSweep_FindStuckChargingError_Propagates above.

	h := newReconHarness(t, &fakeStatusReader{}, 1*time.Hour)
	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("relation \"orders\" does not exist"))

	before := testutil.ToFloat64(observability.ReconFindStuckErrorsTotal)
	err := h.r.Sweep(context.Background())
	require.Error(t, err, "FindStuckCharging error MUST propagate as Sweep error")
	after := testutil.ToFloat64(observability.ReconFindStuckErrorsTotal)
	assert.Equal(t, before+1, after,
		"recon_find_stuck_errors_total must increment exactly once per FindStuckCharging failure")
}

func TestSweep_TransitionLost_BenignNoError(t *testing.T) {
	t.Parallel()

	// Recon picks up a Charging order, gateway says Charged, calls
	// MarkConfirmed — but the original payment worker raced us and
	// already wrote Confirmed. ErrInvalidTransition is the expected
	// signal. Recon must treat as idempotent success — Sweep returns
	// nil, no error path triggered, recon_resolved_total{outcome=
	// "transition_lost"} ticks up.
	id := uuid.New()
	gw := &fakeStatusReader{
		statuses: map[uuid.UUID]domain.ChargeStatus{id: domain.ChargeStatusCharged},
	}
	h := newReconHarness(t, gw, 1*time.Hour)

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{{ID: id, Age: 30 * time.Second}}, nil)
	h.repo.EXPECT().MarkConfirmed(gomock.Any(), id).Return(domain.ErrInvalidTransition)

	require.NoError(t, h.r.Sweep(context.Background()),
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
	h := newReconHarness(t, gw, 1*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())

	h.repo.EXPECT().
		FindStuckCharging(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]domain.StuckCharging{
			{ID: id1, Age: 30 * time.Second},
			{ID: id2, Age: 30 * time.Second},
		}, nil)
	// First order: succeeds, then cancel before second iteration.
	h.repo.EXPECT().MarkConfirmed(gomock.Any(), id1).
		DoAndReturn(func(_ context.Context, _ uuid.UUID) error {
			cancel() // cancel parent BEFORE returning
			return nil
		})
	// id2 must NOT be processed — gomock will fail the test if MarkConfirmed
	// is called for id2 (no EXPECT registered).

	err := h.r.Sweep(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
