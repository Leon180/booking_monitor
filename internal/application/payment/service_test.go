package payment_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/application/payment"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// D4.1 — payment.NewService no longer takes *config.Config; price +
// currency now ride on the Order via `order.AmountCents()` /
// `order.Currency()` (snapshotted at book time by NewReservation).
// Test fixtures construct orders with a meaningful price snapshot via
// makeAwaitingPaymentOrder's `amountCents` / `currency` args.

// ── CreatePaymentIntent (D4) ─────────────────────────────────────────

// makeAwaitingPaymentOrder constructs an order in AwaitingPayment with
// a future reservedUntil + a D4.1 price snapshot. The amountCents +
// currency must be non-zero / non-empty so `order.HasPriceSnapshot()`
// returns true and the production /pay path proceeds; passing 0/""
// would simulate a legacy pre-D4.1 row, which the post-D4.1 service
// correctly rejects.
//
// The synthetic ticket_type_id satisfies the orders.ticket_type_id
// FK column shape; tests don't dereference it (no TicketTypeRepository
// lookup at /pay time — price comes off the order, not the
// ticket_type).
func makeAwaitingPaymentOrder(t *testing.T, orderID, eventID uuid.UUID, quantity int, amountCents int64, currency string) domain.Order {
	t.Helper()
	reservedUntil := time.Now().Add(15 * time.Minute)
	ticketTypeID := uuid.New()
	return domain.ReconstructOrder(
		orderID, 1, eventID, ticketTypeID, quantity,
		domain.OrderStatusAwaitingPayment, time.Now(), reservedUntil,
		"", amountCents, currency,
	)
}

func newPaymentService(t *testing.T) (*gomock.Controller, payment.Service, *mocks.MockPaymentGateway, *mocks.MockOrderRepository) {
	t.Helper()
	ctrl := gomock.NewController(t)
	gw := mocks.NewMockPaymentGateway(ctrl)
	repo := mocks.NewMockOrderRepository(ctrl)
	svc := payment.NewService(gw, repo, mlog.NewNop())
	return ctrl, svc, gw, repo
}

func TestCreatePaymentIntent_HappyPath(t *testing.T) {
	ctrl, svc, gw, repo := newPaymentService(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	eventID := uuid.New()
	// D4.1: amount_cents is the snapshot (already qty × priceCents),
	// frozen on the order at book time. Test passes 4000 explicitly so
	// the gateway expectation pins the same value.
	order := makeAwaitingPaymentOrder(t, orderID, eventID, 2, 4000, "usd")
	wantIntent := domain.PaymentIntent{
		ID:           "pi_test_123",
		ClientSecret: "pi_test_123_secret_abc",
		AmountCents:  4000,
		Currency:     "usd",
	}

	gomock.InOrder(
		repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil),
		gw.EXPECT().CreatePaymentIntent(gomock.Any(), orderID, int64(4000), "usd", gomock.Any()).Return(wantIntent, nil),
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
			order := domain.ReconstructOrder(orderID, 1, uuid.New(), uuid.Nil, 1, tc.status, time.Now(), time.Now().Add(15*time.Minute), "", 0, "")
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
	// Use a real D4.1 price snapshot (2000, "usd") so the test
	// unambiguously exercises the reservedUntil guard, not the
	// HasPriceSnapshot guard. Pre-fix this used (0, "") which would
	// have ALSO failed the snapshot check; fixing makes the assertion
	// intent unambiguous.
	order := domain.ReconstructOrder(orderID, 1, uuid.New(), uuid.New(), 1, domain.OrderStatusAwaitingPayment, time.Now().Add(-16*time.Minute), expiredAt, "", 2000, "usd")
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)

	_, err := svc.CreatePaymentIntent(context.Background(), orderID)
	assert.ErrorIs(t, err, payment.ErrReservationExpired,
		"reservation past TTL must reject with ErrReservationExpired (handler returns 409)")
}

// TestCreatePaymentIntent_RejectsMissingPriceSnapshot pins the D4.1
// data-integrity guard. An order in `awaiting_payment` with a future
// reservedUntil but a missing (amount_cents, currency) snapshot must
// reject with `ErrOrderMissingPriceSnapshot` (NOT
// `ErrOrderNotAwaitingPayment` — the order IS awaiting payment; what's
// missing is the persistence-layer snapshot). Mapped to 409 with a
// distinct public message; deliberately excluded from
// `isExpectedPayError` so the handler logs at Error.
//
// This is the regression net for the legacy-row / migration-gap case
// the defensive `HasPriceSnapshot()` guard was introduced to catch.
// Without this test, a future refactor that flipped the `!` or moved
// the guard would silently regress the data-integrity contract.
func TestCreatePaymentIntent_RejectsMissingPriceSnapshot(t *testing.T) {
	ctrl, svc, gw, repo := newPaymentService(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	// AwaitingPayment + future reservedUntil — only the price snapshot
	// is missing (0/"") so the previous two guards pass and the
	// HasPriceSnapshot guard is the load-bearing rejection.
	order := makeAwaitingPaymentOrder(t, orderID, uuid.New(), 1, 0, "")
	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)
	// gateway.CreatePaymentIntent MUST NOT be called — we never
	// hand a $0 charge to the gateway. gomock fails the test if any
	// unexpected gateway call fires.
	_ = gw

	_, err := svc.CreatePaymentIntent(context.Background(), orderID)
	require.Error(t, err)
	assert.ErrorIs(t, err, payment.ErrOrderMissingPriceSnapshot,
		"missing snapshot must surface as the typed sentinel — distinct from ErrOrderNotAwaitingPayment so handler logs at Error and operator dashboards can branch")
	// Defense in depth: the same call MUST NOT match ErrOrderNotAwaitingPayment.
	// If a future refactor reverts to the old (wrong) sentinel, this
	// guard fires before the public-message regression reaches prod.
	assert.False(t, errors.Is(err, payment.ErrOrderNotAwaitingPayment),
		"missing snapshot must NOT alias to ErrOrderNotAwaitingPayment — review #4 finding C-2 / H-1")
}

func TestCreatePaymentIntent_GatewayError(t *testing.T) {
	ctrl, svc, gw, repo := newPaymentService(t)
	defer ctrl.Finish()

	orderID := uuid.New()
	order := makeAwaitingPaymentOrder(t, orderID, uuid.New(), 1, 2000, "usd")
	gwErr := errors.New("gateway 503")

	repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil)
	gw.EXPECT().CreatePaymentIntent(gomock.Any(), orderID, int64(2000), "usd", gomock.Any()).Return(domain.PaymentIntent{}, gwErr)

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
	order := makeAwaitingPaymentOrder(t, orderID, uuid.New(), 1, 2000, "usd")
	intent := domain.PaymentIntent{ID: "pi_race", ClientSecret: "pi_race_secret", AmountCents: 2000, Currency: "usd"}

	gomock.InOrder(
		repo.EXPECT().GetByID(gomock.Any(), orderID).Return(order, nil),
		gw.EXPECT().CreatePaymentIntent(gomock.Any(), orderID, int64(2000), "usd", gomock.Any()).Return(intent, nil),
		repo.EXPECT().SetPaymentIntentID(gomock.Any(), orderID, "pi_race").Return(domain.ErrOrderNotFound),
	)

	_, err := svc.CreatePaymentIntent(context.Background(), orderID)
	assert.ErrorIs(t, err, domain.ErrOrderNotFound,
		"race-lost on persist must surface ErrOrderNotFound so handler maps to 404 (state changed)")
}
