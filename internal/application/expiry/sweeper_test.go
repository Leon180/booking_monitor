package expiry_test

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
	"booking_monitor/internal/application/expiry"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// recorder captures every Metrics call so tests assert exact label
// values + call ordering. Mirrors the saga watchdog test pattern.
type recorder struct {
	resolved          []string
	maxAgeExceeded    int
	findExpiredErrs   int
	oldestOverdue     []float64
	backlogAfterSweep []int
	resolveDur        []float64
	sweepDur          []float64
	resolveAge        []float64
}

func (r *recorder) IncResolved(o string)              { r.resolved = append(r.resolved, o) }
func (r *recorder) IncMaxAgeExceeded()                { r.maxAgeExceeded++ }
func (r *recorder) IncFindExpiredErrors()             { r.findExpiredErrs++ }
func (r *recorder) SetOldestOverdueAge(s float64)     { r.oldestOverdue = append(r.oldestOverdue, s) }
func (r *recorder) SetBacklogAfterSweep(c int)        { r.backlogAfterSweep = append(r.backlogAfterSweep, c) }
func (r *recorder) ObserveResolveDuration(s float64)  { r.resolveDur = append(r.resolveDur, s) }
func (r *recorder) ObserveSweepDuration(s float64)    { r.sweepDur = append(r.sweepDur, s) }
func (r *recorder) ObserveResolveAge(s float64)       { r.resolveAge = append(r.resolveAge, s) }

// fixture stamps out the standard sweeper-under-test wiring. The
// caller installs EXPECT()s on `repo` + `uow` per scenario.
func fixture(t *testing.T) (*gomock.Controller, *expiry.Sweeper, *mocks.MockOrderRepository, *mocks.MockUnitOfWork, *recorder) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	rec := &recorder{}
	s := expiry.NewSweeper(repo, uow, expiry.Config{
		SweepInterval:     30 * time.Second,
		ExpiryGracePeriod: 2 * time.Second,
		MaxAge:            24 * time.Hour,
		BatchSize:         100,
	}, rec, mlog.NewNop())
	return ctrl, s, repo, uow, rec
}

func awaitingOrder(orderID, eventID uuid.UUID, reservedUntil time.Time) domain.Order {
	return domain.ReconstructOrder(
		orderID, 1, eventID, uuid.New(), 1,
		domain.OrderStatusAwaitingPayment,
		time.Now().Add(-20*time.Minute), reservedUntil,
		"pi_test", 4000, "usd",
	)
}

// uowDoInvokes wires MockUnitOfWork.Do to actually execute the closure
// against the supplied Repositories bundle. Mirrors the precedent in
// internal/application/payment/service_test.go.
func uowDoInvokes(uow *mocks.MockUnitOfWork, repos *application.Repositories, returnErr error) {
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			if err := fn(repos); err != nil {
				return err
			}
			return returnErr
		},
	)
}

// expectCountSucceeds — most tests don't care about the post-sweep
// count, just that it ran cleanly. This helper installs the
// expectation in one line.
func expectCountSucceeds(repo *mocks.MockOrderRepository, count int, oldest time.Duration) {
	repo.EXPECT().CountOverdueAfterCutoff(gomock.Any(), gomock.Any()).Return(count, oldest, nil)
}

// ── happy paths ──────────────────────────────────────────────────────

func TestSweep_HappyPath_ExpiresAndEmits(t *testing.T) {
	ctrl, s, repo, uow, rec := fixture(t)
	defer ctrl.Finish()

	orderID, eventID := uuid.New(), uuid.New()
	row := domain.ExpiredReservation{ID: orderID, Age: 10 * time.Second}
	order := awaitingOrder(orderID, eventID, time.Now().Add(-10*time.Second))

	repo.EXPECT().FindExpiredReservations(gomock.Any(), 2*time.Second, 100).Return([]domain.ExpiredReservation{row}, nil)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	outbox := mocks.NewMockOutboxRepository(ctrl)
	repos := &application.Repositories{Order: repo, Outbox: outbox}
	repo.EXPECT().MarkExpired(gomock.Any(), orderID).Return(nil)
	outbox.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.OutboxEvent{}, nil)
	uowDoInvokes(uow, repos, nil)

	expectCountSucceeds(repo, 0, 0)

	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, []string{"expired"}, rec.resolved)
	assert.Equal(t, 0, rec.maxAgeExceeded)
	assert.Equal(t, 0, rec.findExpiredErrs)
	assert.Equal(t, []int{0}, rec.backlogAfterSweep)
	assert.Len(t, rec.resolveDur, 1)
	assert.Len(t, rec.sweepDur, 1)
}

// MaxAge labeling — round-1 P1 fix proof: row IS expired, AND the
// dedicated max-age counter increments AND the outcome label is
// `expired_overaged` (not `expired`).
func TestSweep_OverAged_StillExpiresWithMaxAgeLabel(t *testing.T) {
	ctrl, s, repo, uow, rec := fixture(t)
	defer ctrl.Finish()

	orderID, eventID := uuid.New(), uuid.New()
	row := domain.ExpiredReservation{ID: orderID, Age: 25 * time.Hour}
	order := awaitingOrder(orderID, eventID, time.Now().Add(-25*time.Hour))

	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return([]domain.ExpiredReservation{row}, nil)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	outbox := mocks.NewMockOutboxRepository(ctrl)
	repos := &application.Repositories{Order: repo, Outbox: outbox}
	repo.EXPECT().MarkExpired(gomock.Any(), orderID).Return(nil)
	outbox.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.OutboxEvent{}, nil)
	uowDoInvokes(uow, repos, nil)
	expectCountSucceeds(repo, 0, 0)

	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, []string{"expired_overaged"}, rec.resolved,
		"overaged rows MUST still be expired, only the outcome label changes (round-1 P1)")
	assert.Equal(t, 1, rec.maxAgeExceeded,
		"max-age counter increments alongside expired_overaged outcome")
}

// ── per-row failure branches ────────────────────────────────────────

func TestSweep_AlreadyTerminal_PreUoWStatusGuard(t *testing.T) {
	ctrl, s, repo, _, rec := fixture(t)
	defer ctrl.Finish()

	orderID, eventID := uuid.New(), uuid.New()
	row := domain.ExpiredReservation{ID: orderID, Age: 5 * time.Second}
	// Order has already been moved to `paid` by D5 between the find
	// query and our GetByID — pre-UoW status guard catches it.
	paid := domain.ReconstructOrder(
		orderID, 1, eventID, uuid.New(), 1,
		domain.OrderStatusPaid,
		time.Now().Add(-20*time.Minute), time.Now().Add(-5*time.Second),
		"pi_test", 4000, "usd",
	)

	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return([]domain.ExpiredReservation{row}, nil)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(paid, nil)
	expectCountSucceeds(repo, 0, 0)

	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, []string{"already_terminal"}, rec.resolved,
		"pre-UoW status flip → benign skip, no order.failed emit")
}

func TestSweep_AlreadyTerminal_UoWErrInvalidTransition(t *testing.T) {
	ctrl, s, repo, uow, rec := fixture(t)
	defer ctrl.Finish()

	orderID, eventID := uuid.New(), uuid.New()
	row := domain.ExpiredReservation{ID: orderID, Age: 5 * time.Second}
	order := awaitingOrder(orderID, eventID, time.Now().Add(-5*time.Second))

	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return([]domain.ExpiredReservation{row}, nil)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	// D5 wins inside the UoW — MarkExpired returns ErrInvalidTransition.
	repos := &application.Repositories{Order: repo}
	repo.EXPECT().MarkExpired(gomock.Any(), orderID).Return(domain.ErrInvalidTransition)
	uowDoInvokes(uow, repos, nil)
	expectCountSucceeds(repo, 0, 0)

	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, []string{"already_terminal"}, rec.resolved,
		"UoW-internal D5 race → already_terminal (NOT transition_error)")
}

func TestSweep_GetByIDError(t *testing.T) {
	ctrl, s, repo, _, rec := fixture(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	row := domain.ExpiredReservation{ID: orderID, Age: 5 * time.Second}
	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return([]domain.ExpiredReservation{row}, nil)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(domain.Order{}, errors.New("db connection refused"))
	expectCountSucceeds(repo, 0, 0)

	require.NoError(t, s.Sweep(context.Background()), "per-row failure must NOT abort the sweep")
	assert.Equal(t, []string{"getbyid_error"}, rec.resolved)
}

func TestSweep_TransitionError(t *testing.T) {
	ctrl, s, repo, uow, rec := fixture(t)
	defer ctrl.Finish()

	orderID, eventID := uuid.New(), uuid.New()
	row := domain.ExpiredReservation{ID: orderID, Age: 5 * time.Second}
	order := awaitingOrder(orderID, eventID, time.Now().Add(-5*time.Second))

	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return([]domain.ExpiredReservation{row}, nil)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	repos := &application.Repositories{Order: repo}
	repo.EXPECT().MarkExpired(gomock.Any(), orderID).Return(errors.New("db lock timeout"))
	uowDoInvokes(uow, repos, nil)
	expectCountSucceeds(repo, 0, 0)

	require.NoError(t, s.Sweep(context.Background()))
	assert.Equal(t, []string{"transition_error"}, rec.resolved,
		"non-ErrInvalidTransition errors classified as transition_error (will retry next sweep)")
}

// ── batch + ordering ─────────────────────────────────────────────────

// Round-3 F2 + multi-agent review fix: gauges are exclusively
// written from the post-sweep CountOverdueAfterCutoff result. No
// mid-sweep input view, so a count-query failure leaves the gauge
// at the previous sweep's last-known-good (NOT corrupted with this
// tick's input snapshot).
func TestSweep_OldestGaugeFromPostSweepCount(t *testing.T) {
	ctrl, s, repo, _, rec := fixture(t)
	defer ctrl.Finish()

	// Three rows in input. Per-row resolves all error out (irrelevant
	// for this test — we only care about the gauge contract).
	rows := []domain.ExpiredReservation{
		{ID: uuid.New(), Age: 30 * time.Second},
		{ID: uuid.New(), Age: 10 * time.Second},
		{ID: uuid.New(), Age: 5 * time.Second},
	}
	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, nil)
	for _, r := range rows {
		repo.EXPECT().GetByID(gomock.Any(), r.ID).Return(domain.Order{}, errors.New("transient"))
	}

	// Post-sweep count returns 7 rows still overdue with oldest = 45s.
	// (Plausible scenario: BatchSize was smaller than the real
	// backlog, so 7 rows the sweeper couldn't reach this tick remain.)
	expectCountSucceeds(repo, 7, 45*time.Second)

	require.NoError(t, s.Sweep(context.Background()))

	require.Len(t, rec.oldestOverdue, 1,
		"gauge must be written exactly once per sweep — only from post-sweep count, NOT from the sweep-start input view (round-3 F2 contract)")
	assert.Equal(t, 45.0, rec.oldestOverdue[0],
		"gauge value must come from CountOverdueAfterCutoff (post-sweep view), NOT from FindExpiredReservations[0].Age (sweep-start input view)")
	assert.Equal(t, []int{7}, rec.backlogAfterSweep,
		"backlog gauge captures rows still overdue after this batch")
}

// ── Sweep-level error contracts ──────────────────────────────────────

func TestSweep_FindExpiredError_ReturnsErrAndIncrementsCounter(t *testing.T) {
	ctrl, s, repo, _, rec := fixture(t)
	defer ctrl.Finish()

	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("query timeout"))
	// Note: NO CountOverdueAfterCutoff expectation — Sweep returns before reaching it.

	err := s.Sweep(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, rec.findExpiredErrs, "find-error counter increments on FindExpiredReservations failure")
	assert.Empty(t, rec.resolved)
	assert.Empty(t, rec.backlogAfterSweep, "no backlog gauge update — Sweep aborted before count")
}

// Round-3 F2 contract proof: count-query failure increments the SAME
// counter, leaves gauges at last-known-good (no SetBacklogAfterSweep
// call), AND Sweep returns the error so loop mode logs+continues /
// --once mode exits 1.
func TestSweep_CountOverdueError_GaugesHeldAndSweepReturnsErr(t *testing.T) {
	ctrl, s, repo, uow, rec := fixture(t)
	defer ctrl.Finish()

	orderID, eventID := uuid.New(), uuid.New()
	row := domain.ExpiredReservation{ID: orderID, Age: 5 * time.Second}
	order := awaitingOrder(orderID, eventID, time.Now().Add(-5*time.Second))

	// Per-row resolve succeeds — the failure is purely in the
	// post-sweep observability query.
	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return([]domain.ExpiredReservation{row}, nil)
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	outbox := mocks.NewMockOutboxRepository(ctrl)
	repos := &application.Repositories{Order: repo, Outbox: outbox}
	repo.EXPECT().MarkExpired(gomock.Any(), orderID).Return(nil)
	outbox.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.OutboxEvent{}, nil)
	uowDoInvokes(uow, repos, nil)

	repo.EXPECT().CountOverdueAfterCutoff(gomock.Any(), gomock.Any()).Return(0, time.Duration(0), errors.New("query timeout"))

	err := s.Sweep(context.Background())
	require.Error(t, err, "count-query failure MUST surface as Sweep error (--once mode then exits 1)")
	assert.Equal(t, 1, rec.findExpiredErrs, "count-query failure shares the find-error counter")
	assert.Equal(t, []string{"expired"}, rec.resolved,
		"already-resolved rows are NOT rolled back on count-query failure")
	// Round-3 F2 + multi-agent review fix: BOTH backlog AND oldest-
	// overdue gauges MUST be left at last-known-good when count
	// fails. Pre-fix the sweep-start input snapshot wrote into
	// `oldestOverdue` before the count failure, leaving the gauge
	// at this-cycle's input view (stale) instead of the previous
	// successful sweep's output view (true last-known-good).
	assert.Empty(t, rec.backlogAfterSweep,
		"backlog gauge MUST be left at last-known-good when count fails (round-3 F2 contract)")
	assert.Empty(t, rec.oldestOverdue,
		"oldest-overdue gauge MUST be left at last-known-good when count fails — NO mid-sweep input-view write")
}

// ── ctx cancellation ─────────────────────────────────────────────────

func TestSweep_CtxCancellation_StopsAtLoopBoundary(t *testing.T) {
	ctrl, s, repo, _, _ := fixture(t)
	defer ctrl.Finish()

	rows := []domain.ExpiredReservation{
		{ID: uuid.New(), Age: 5 * time.Second},
		{ID: uuid.New(), Age: 5 * time.Second},
	}
	repo.EXPECT().FindExpiredReservations(gomock.Any(), gomock.Any(), gomock.Any()).Return(rows, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE the for-loop runs

	err := s.Sweep(ctx)
	assert.ErrorIs(t, err, context.Canceled,
		"cancelled ctx must surface as the Sweep error so loop exits cleanly")
}

// ── exhaustive outcome coverage assertion ───────────────────────────

func TestAllOutcomes_CoverageMatrix(t *testing.T) {
	// Defense-in-depth: AllOutcomes must enumerate every literal
	// passed to IncResolved. If a future commit adds a new outcome
	// label without updating the AllOutcomes slice, the bootstrap
	// adapter's pre-warm misses it and dashboards show "no data" for
	// the new label until the first event fires.
	expected := map[string]bool{
		"expired": true, "expired_overaged": true, "already_terminal": true,
		"getbyid_error": true, "marshal_error": true, "outbox_error": true,
		"transition_error": true,
	}
	for _, o := range expiry.AllOutcomes {
		require.True(t, expected[o], "unexpected outcome in AllOutcomes: %q", o)
		delete(expected, o)
	}
	require.Empty(t, expected, "AllOutcomes is missing values: %v", expected)
}
