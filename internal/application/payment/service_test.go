package payment_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/payment"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// testPaymentConfig builds a *config.Config carrying just the
// BookingConfig the payment service reads at NewService time. Kept
// minimal so tests don't have to think about unrelated subsystems.
func testPaymentConfig() *config.Config {
	return &config.Config{
		Booking: config.BookingConfig{
			DefaultTicketPriceCents: 2000,
			DefaultCurrency:         "usd",
		},
	}
}

// makeOrder produces a Pending order matching the supplied event so the
// idempotency-check GetByID has something realistic to return.
//
// Tests need to fabricate orders in any status (including transitions
// that don't exist in the legal graph — Confirmed/Compensated for
// idempotency-skip cases). ReconstructOrder is the right tool — it
// bypasses both NewOrder's invariant checks AND the typed transition
// state machine, mirroring how the postgres scan path rehydrates
// arbitrary persisted state from a row.
func makeOrder(t *testing.T, ev *application.OrderCreatedEvent, status domain.OrderStatus) domain.Order {
	t.Helper()
	orderID, err := uuid.NewV7()
	require.NoError(t, err)
	return domain.ReconstructOrder(orderID, 1, ev.EventID, 1, status, time.Now(), time.Time{}, "")
}

func newEvent(t *testing.T) *application.OrderCreatedEvent {
	t.Helper()
	orderID, err := uuid.NewV7()
	require.NoError(t, err)
	eventID, err := uuid.NewV7()
	require.NoError(t, err)
	return &application.OrderCreatedEvent{
		OrderID: orderID,
		EventID: eventID,
		UserID:  1,
		Amount:  100,
		Version: application.OrderEventVersion,
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
	svc := payment.NewService(gw, repo, uow, testPaymentConfig(), mlog.NewNop())

	err := svc.ProcessOrder(context.Background(), &application.OrderCreatedEvent{OrderID: uuid.Nil, Amount: 1})

	assert.ErrorIs(t, err, payment.ErrInvalidPaymentEvent)
}

func TestProcessOrder_RejectsNegativeAmount(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	svc := payment.NewService(gw, repo, uow, testPaymentConfig(), mlog.NewNop())

	ev := newEvent(t)
	ev.Amount = -1

	err := svc.ProcessOrder(context.Background(), ev)
	assert.ErrorIs(t, err, payment.ErrInvalidPaymentEvent)
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

	svc := payment.NewService(gw, repo, uow, testPaymentConfig(), mlog.NewNop())
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
		// A4: MarkCharging is the new step BEFORE Charge — intent log
		// for the recon subcommand to recover stuck-mid-flight orders.
		repo.EXPECT().MarkCharging(gomock.Any(), ev.OrderID).Return(nil),
		gw.EXPECT().Charge(gomock.Any(), ev.OrderID, ev.Amount).Return(nil),
		repo.EXPECT().MarkConfirmed(gomock.Any(), ev.OrderID).Return(nil),
	)

	svc := payment.NewService(gw, repo, uow, testPaymentConfig(), mlog.NewNop())
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
		// First attempt — A4 inserts MarkCharging before Charge.
		// We assume MarkCharging succeeds; the failure-of-Charging
		// race is covered by reconciler tests.
		repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusPending), nil),
		repo.EXPECT().MarkCharging(gomock.Any(), ev.OrderID).Return(nil),
		gw.EXPECT().Charge(gomock.Any(), ev.OrderID, ev.Amount).Return(nil),
		repo.EXPECT().MarkConfirmed(gomock.Any(), ev.OrderID).Return(dbErr),
		// Second attempt — idempotency check sees Charging (NOT Pending,
		// because MarkCharging committed on the first attempt). After
		// A4, Charging triggers the "skip — let the reconciler handle"
		// branch (see service.go comment). Service returns nil; Kafka
		// commits the offset; the reconciler resolves via gateway.GetStatus.
		repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusCharging), nil),
	)

	svc := payment.NewService(gw, repo, uow, testPaymentConfig(), mlog.NewNop())

	err := svc.ProcessOrder(context.Background(), ev)
	assert.ErrorIs(t, err, dbErr, "first attempt surfaces the DB error so Kafka retries")

	// A4 changes the recovery model: on Kafka redelivery the service
	// sees status=Charging (the intent log) and SKIPS — the reconciler
	// resolves via gateway.GetStatus rather than the worker re-entering
	// the Charge path. Behaviourally equivalent (no double-charge,
	// final state reaches Confirmed) but the responsibility is split
	// between worker (forward path) and reconciler (recovery path).
	err = svc.ProcessOrder(context.Background(), ev)
	assert.NoError(t, err, "second attempt sees Charging status, skips to let reconciler handle")
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
		// A4: MarkCharging commits BEFORE Charge — its tx is independent
		// of the saga uow, so it survives the saga uow.Do failure.
		repo.EXPECT().MarkCharging(gomock.Any(), ev.OrderID).Return(nil),
		gw.EXPECT().Charge(gomock.Any(), ev.OrderID, ev.Amount).Return(chargeErr),
		// uow.Do invokes the closure: MarkFailed + outbox.Create.
		// Note MarkFailed is called from Charging→Failed (the widened
		// transition); the failure flow goes through the same code
		// path as Pending→Failed.
		repo.EXPECT().MarkFailed(gomock.Any(), ev.OrderID).Return(nil),
		outbox.EXPECT().Create(gomock.Any(), gomock.AssignableToTypeOf(domain.OutboxEvent{})).
			DoAndReturn(func(_ context.Context, oe domain.OutboxEvent) (domain.OutboxEvent, error) {
				assert.Equal(t, domain.EventTypeOrderFailed, oe.EventType())
				var p application.OrderFailedEvent
				require.NoError(t, json.Unmarshal(oe.Payload(), &p))
				assert.Equal(t, ev.OrderID, p.OrderID)
				return oe, nil
			}),
		// Second attempt — A4 changes the recovery model. The first
		// attempt left the row at status=Charging (MarkCharging
		// committed; saga uow rolled back so MarkFailed didn't apply).
		// Service sees Charging on retry → SKIP → reconciler resolves
		// via gateway.GetStatus.
		repo.EXPECT().GetByID(gomock.Any(), ev.OrderID).Return(makeOrder(t, ev, domain.OrderStatusCharging), nil),
	)

	// First attempt: closure runs, but the uow itself "fails to commit"
	expectUowDoInvokesFn(uow, repos, uowFirstAttemptErr)
	// Second attempt no longer invokes uow.Do (we skip on Charging),
	// so no second expectUowDoInvokesFn needed.

	svc := payment.NewService(gw, repo, uow, testPaymentConfig(), mlog.NewNop())

	err := svc.ProcessOrder(context.Background(), ev)
	assert.ErrorIs(t, err, uowFirstAttemptErr, "first attempt surfaces uow error so Kafka retries")

	err = svc.ProcessOrder(context.Background(), ev)
	assert.NoError(t, err, "second attempt sees Charging status, skips to let reconciler handle")
}

// ── CreatePaymentIntent (D4) ─────────────────────────────────────────

// makeAwaitingPaymentOrder constructs an order in AwaitingPayment with
// a future reservedUntil — the canonical input shape for /pay.
func makeAwaitingPaymentOrder(t *testing.T, orderID, eventID uuid.UUID, quantity int) domain.Order {
	t.Helper()
	reservedUntil := time.Now().Add(15 * time.Minute)
	return domain.ReconstructOrder(orderID, 1, eventID, quantity, domain.OrderStatusAwaitingPayment, time.Now(), reservedUntil, "")
}

func newPaymentService(t *testing.T) (*gomock.Controller, payment.Service, *mocks.MockPaymentGateway, *mocks.MockOrderRepository) {
	t.Helper()
	ctrl := gomock.NewController(t)
	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)
	svc := payment.NewService(gw, repo, uow, testPaymentConfig(), mlog.NewNop())
	return ctrl, svc, gw, repo
}

func TestCreatePaymentIntent_HappyPath(t *testing.T) {
	ctrl, svc, gw, repo := newPaymentService(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	order := makeAwaitingPaymentOrder(t, orderID, eventID, 2) // qty 2 → 4000 cents at price=2000
	wantIntent := domain.PaymentIntent{
		ID:           "pi_test_123",
		ClientSecret: "pi_test_123_secret_abc",
		AmountCents:  4000,
		Currency:     "usd",
	}

	gomock.InOrder(
		repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil),
		gw.EXPECT().CreatePaymentIntent(gomock.Any(), orderID, int64(4000), "usd").Return(wantIntent, nil),
		repo.EXPECT().SetPaymentIntentID(gomock.Any(), orderID, "pi_test_123").Return(nil),
	)

	got, err := svc.CreatePaymentIntent(context.Background(), orderID)
	require.NoError(t, err)
	assert.Equal(t, wantIntent, got, "service must return the gateway-issued intent verbatim")
}

func TestCreatePaymentIntent_RejectsZeroOrderID(t *testing.T) {
	ctrl, svc, _, _ := newPaymentService(t)
	defer ctrl.Finish()

	_, err := svc.CreatePaymentIntent(context.Background(), uuid.Nil)
	assert.ErrorIs(t, err, domain.ErrInvalidOrderID,
		"zero UUID must short-circuit before any repo / gateway call")
}

func TestCreatePaymentIntent_OrderNotFound(t *testing.T) {
	ctrl, svc, _, repo := newPaymentService(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(domain.Order{}, domain.ErrOrderNotFound)

	_, err := svc.CreatePaymentIntent(context.Background(), orderID)
	assert.ErrorIs(t, err, domain.ErrOrderNotFound,
		"missing order must surface ErrOrderNotFound unwrapped so the handler maps to 404")
}

// TestCreatePaymentIntent_RejectsNonAwaitingPaymentStatuses pins the
// state-machine guard: only AwaitingPayment orders permit /pay.
// Every other status (terminal or not) must reject with
// ErrOrderNotAwaitingPayment so the handler returns 409 — and gateway
// MUST NOT be called (gomock fails the test if it is).
func TestCreatePaymentIntent_RejectsNonAwaitingPaymentStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status domain.OrderStatus
	}{
		{"Pending", domain.OrderStatusPending},
		{"Charging", domain.OrderStatusCharging},
		{"Confirmed", domain.OrderStatusConfirmed},
		{"Failed", domain.OrderStatusFailed},
		{"Compensated", domain.OrderStatusCompensated},
		{"Paid", domain.OrderStatusPaid},
		{"Expired", domain.OrderStatusExpired},
		{"PaymentFailed", domain.OrderStatusPaymentFailed},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctrl, svc, _, repo := newPaymentService(t)
			defer ctrl.Finish()

			orderID := uuid.New()
			order := domain.ReconstructOrder(orderID, 1, uuid.New(), 1, tc.status, time.Now(), time.Now().Add(15*time.Minute), "")
			repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

			_, err := svc.CreatePaymentIntent(context.Background(), orderID)
			assert.ErrorIs(t, err, payment.ErrOrderNotAwaitingPayment,
				"only AwaitingPayment permits /pay; %s must reject", tc.status)
		})
	}
}

func TestCreatePaymentIntent_RejectsExpiredReservation(t *testing.T) {
	ctrl, svc, _, repo := newPaymentService(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	expiredAt := time.Now().Add(-1 * time.Minute)
	order := domain.ReconstructOrder(orderID, 1, uuid.New(), 1, domain.OrderStatusAwaitingPayment, time.Now().Add(-16*time.Minute), expiredAt, "")
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	_, err := svc.CreatePaymentIntent(context.Background(), orderID)
	assert.ErrorIs(t, err, payment.ErrReservationExpired,
		"reservation past TTL must reject with ErrReservationExpired (handler returns 409)")
}

func TestCreatePaymentIntent_GatewayError(t *testing.T) {
	ctrl, svc, gw, repo := newPaymentService(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	order := makeAwaitingPaymentOrder(t, orderID, uuid.New(), 1)
	gwErr := errors.New("gateway 503")

	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)
	gw.EXPECT().CreatePaymentIntent(gomock.Any(), orderID, int64(2000), "usd").Return(domain.PaymentIntent{}, gwErr)

	_, err := svc.CreatePaymentIntent(context.Background(), orderID)
	require.Error(t, err)
	assert.ErrorIs(t, err, gwErr, "gateway error wraps through with errors.Is reachability")
}

// TestCreatePaymentIntent_PersistRaceLost: race window between the
// service's GetByID (saw AwaitingPayment) and SetPaymentIntentID
// (DB-level predicate rejects because the webhook flipped status to
// Paid). Surface as ErrOrderNotFound so the handler maps cleanly.
func TestCreatePaymentIntent_PersistRaceLost(t *testing.T) {
	ctrl, svc, gw, repo := newPaymentService(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	order := makeAwaitingPaymentOrder(t, orderID, uuid.New(), 1)
	intent := domain.PaymentIntent{ID: "pi_race", ClientSecret: "pi_race_secret", AmountCents: 2000, Currency: "usd"}

	gomock.InOrder(
		repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil),
		gw.EXPECT().CreatePaymentIntent(gomock.Any(), orderID, int64(2000), "usd").Return(intent, nil),
		repo.EXPECT().SetPaymentIntentID(gomock.Any(), orderID, "pi_race").Return(domain.ErrOrderNotFound),
	)

	_, err := svc.CreatePaymentIntent(context.Background(), orderID)
	assert.ErrorIs(t, err, domain.ErrOrderNotFound,
		"race-lost on persist must surface ErrOrderNotFound so handler maps to 404 (state changed)")
}
