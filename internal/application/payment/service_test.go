package payment_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/payment"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// makeOrder produces a Pending order matching the supplied event so the
// idempotency-check GetByID has something realistic to return.
func makeOrder(t *testing.T, ev *domain.OrderCreatedEvent, status domain.OrderStatus) domain.Order {
	t.Helper()
	o, err := domain.NewOrder(1, ev.EventID, 1)
	require.NoError(t, err)
	if status != domain.OrderStatusPending {
		o = o.WithStatus(status)
	}
	return o
}

func newEvent(t *testing.T) *domain.OrderCreatedEvent {
	t.Helper()
	orderID, err := uuid.NewV7()
	require.NoError(t, err)
	eventID, err := uuid.NewV7()
	require.NoError(t, err)
	return &domain.OrderCreatedEvent{
		OrderID: orderID,
		EventID: eventID,
		UserID:  1,
		Amount:  100,
		Version: domain.OrderEventVersion,
	}
}

// expectUowDoInvokesFn wires MockUnitOfWork.Do to actually invoke its fn
// argument with a Repositories bundle of the supplied mocks. Mirrors the
// pattern in worker_service_test.go.
func expectUowDoInvokesFn(uow *mocks.MockUnitOfWork, repos *application.Repositories, returnErr error) *gomock.Call {
	return uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, fn func(*application.Repositories) error) error {
			if err := fn(repos); err != nil {
				return err
			}
			return returnErr
		})
}

// ── Validation paths ─────────────────────────────────────────────────

func TestProcessOrder_RejectsZeroOrderID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	svc := payment.NewService(gw, repo, uow, mlog.NewNop())

	err := svc.ProcessOrder(context.Background(), &domain.OrderCreatedEvent{OrderID: uuid.Nil, Amount: 1})

	assert.ErrorIs(t, err, domain.ErrInvalidPaymentEvent)
}

func TestProcessOrder_RejectsNegativeAmount(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	svc := payment.NewService(gw, repo, uow, mlog.NewNop())

	ev := newEvent(t)
	ev.Amount = -1

	err := svc.ProcessOrder(context.Background(), ev)
	assert.ErrorIs(t, err, domain.ErrInvalidPaymentEvent)
}

// ── Idempotency short-circuit (order already processed) ─────────────

func TestProcessOrder_AlreadyConfirmedSkipsCharge(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	ev := newEvent(t)

	repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusConfirmed), nil)
	// gateway.Charge MUST NOT be called for an already-processed order.
	// gomock fails the test if any unexpected call fires.

	svc := payment.NewService(gw, repo, uow, mlog.NewNop())
	assert.NoError(t, svc.ProcessOrder(context.Background(), ev))
}

// ── Happy path ──────────────────────────────────────────────────────

func TestProcessOrder_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	ev := newEvent(t)

	gomock.InOrder(
		repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusPending), nil),
		gw.EXPECT().Charge(gomock.Any(), ev.OrderID, ev.Amount).Return(nil),
		repo.EXPECT().UpdateStatus(gomock.Any(), ev.OrderID, domain.OrderStatusConfirmed).Return(nil),
	)

	svc := payment.NewService(gw, repo, uow, mlog.NewNop())
	assert.NoError(t, svc.ProcessOrder(context.Background(), ev))
}

// ── Race case b: Charge OK, then UpdateStatus(Confirmed) fails ──────
//
// First attempt:  Charge OK → UpdateStatus(Confirmed) fails → return err
// Kafka redelivers. Idempotency check sees Status=Pending (because the
// UpdateStatus failed). Service re-enters Charge path. The IDEMPOTENT
// gateway returns the cached success — no double charge.
// Second attempt: UpdateStatus(Confirmed) succeeds → return nil.
//
// This test verifies the SERVICE behaves correctly given an idempotent
// gateway. Mock returns (nil, nil) for both Charge calls, exactly as a
// real Stripe-style gateway would for a cached success.
func TestProcessOrder_RetryAfterUpdateStatusFailure_ProducesSingleConfirm(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	ev := newEvent(t)

	dbErr := errors.New("conn reset by peer")

	gomock.InOrder(
		// First attempt
		repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusPending), nil),
		gw.EXPECT().Charge(gomock.Any(), ev.OrderID, ev.Amount).Return(nil),
		repo.EXPECT().UpdateStatus(gomock.Any(), ev.OrderID, domain.OrderStatusConfirmed).Return(dbErr),
		// Second attempt — idempotency check still sees Pending
		repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusPending), nil),
		// Charge called again with the same orderID → idempotent gateway returns the cached success
		gw.EXPECT().Charge(gomock.Any(), ev.OrderID, ev.Amount).Return(nil),
		repo.EXPECT().UpdateStatus(gomock.Any(), ev.OrderID, domain.OrderStatusConfirmed).Return(nil),
	)

	svc := payment.NewService(gw, repo, uow, mlog.NewNop())

	err := svc.ProcessOrder(context.Background(), ev)
	assert.ErrorIs(t, err, dbErr, "first attempt surfaces the DB error so Kafka retries")

	err = svc.ProcessOrder(context.Background(), ev)
	assert.NoError(t, err, "second attempt completes the Confirm transition")
}

// ── Race case a: Charge fails, then saga uow.Do fails ───────────────
//
// First attempt:  Charge fail → uow.Do fail → return err
// Kafka redelivers. Idempotency check sees Status=Pending (UpdateStatus
// inside the uow never committed). Service re-enters Charge path. The
// IDEMPOTENT gateway returns the SAME failure verdict as the first call
// — no inconsistent state where one retry succeeded and one failed.
// Second attempt: uow.Do succeeds → outbox order.failed written →
// saga compensator picks it up downstream.
func TestProcessOrder_RetryAfterChargeFailureAndSagaFailure_StableFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	outbox := mocks.NewMockOutboxRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	ev := newEvent(t)

	chargeErr := errors.New("payment declined")
	uowFirstAttemptErr := errors.New("outbox write conn lost")

	repos := &application.Repositories{Order: repo, Outbox: outbox}

	gomock.InOrder(
		// First attempt
		repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusPending), nil),
		gw.EXPECT().Charge(gomock.Any(), ev.OrderID, ev.Amount).Return(chargeErr),
		// uow.Do invokes the closure: UpdateStatus(Failed) + outbox.Create
		repo.EXPECT().UpdateStatus(gomock.Any(), ev.OrderID, domain.OrderStatusFailed).Return(nil),
		outbox.EXPECT().Create(gomock.Any(), gomock.AssignableToTypeOf(domain.OutboxEvent{})).
			DoAndReturn(func(_ context.Context, oe domain.OutboxEvent) (domain.OutboxEvent, error) {
				assert.Equal(t, domain.EventTypeOrderFailed, oe.EventType())
				var p domain.OrderFailedEvent
				require.NoError(t, json.Unmarshal(oe.Payload(), &p))
				assert.Equal(t, ev.OrderID, p.OrderID)
				return oe, nil
			}),
		// Second attempt — Pending again (uow rolled back)
		repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusPending), nil),
		// Idempotent gateway returns SAME failure
		gw.EXPECT().Charge(gomock.Any(), ev.OrderID, ev.Amount).Return(chargeErr),
		// uow.Do invokes closure successfully this time
		repo.EXPECT().UpdateStatus(gomock.Any(), ev.OrderID, domain.OrderStatusFailed).Return(nil),
		outbox.EXPECT().Create(gomock.Any(), gomock.AssignableToTypeOf(domain.OutboxEvent{})).
			Return(domain.OutboxEvent{}, nil),
	)

	// First attempt: closure returns nil, but the uow itself "fails to commit"
	expectUowDoInvokesFn(uow, repos, uowFirstAttemptErr)
	// Second attempt: closure returns nil, uow commits cleanly
	expectUowDoInvokesFn(uow, repos, nil)

	svc := payment.NewService(gw, repo, uow, mlog.NewNop())

	err := svc.ProcessOrder(context.Background(), ev)
	assert.ErrorIs(t, err, uowFirstAttemptErr, "first attempt surfaces uow error so Kafka retries")

	err = svc.ProcessOrder(context.Background(), ev)
	assert.NoError(t, err, "second attempt successfully writes the saga compensating event")
}
