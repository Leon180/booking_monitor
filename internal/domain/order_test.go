package domain_test

import (
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/domain"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOrder(t *testing.T) {
	t.Parallel()

	validOrderID := uuid.New()
	validEventID := uuid.New()

	tests := []struct {
		name      string
		orderID   uuid.UUID
		userID    int
		eventID   uuid.UUID
		quantity  int
		wantErr   error
		assertSet bool
	}{
		{name: "Valid", orderID: validOrderID, userID: 1, eventID: validEventID, quantity: 1, assertSet: true},
		{name: "Valid larger qty", orderID: uuid.New(), userID: 42, eventID: uuid.New(), quantity: 3, assertSet: true},
		{name: "Zero orderID (uuid.Nil) rejected", orderID: uuid.Nil, userID: 1, eventID: validEventID, quantity: 1, wantErr: domain.ErrInvalidOrderID},
		{name: "Zero userID rejected", orderID: validOrderID, userID: 0, eventID: validEventID, quantity: 1, wantErr: domain.ErrInvalidUserID},
		{name: "Negative userID rejected", orderID: validOrderID, userID: -1, eventID: validEventID, quantity: 1, wantErr: domain.ErrInvalidUserID},
		{name: "Zero eventID (uuid.Nil) rejected", orderID: validOrderID, userID: 1, eventID: uuid.Nil, quantity: 1, wantErr: domain.ErrInvalidEventID},
		{name: "Zero quantity rejected", orderID: validOrderID, userID: 1, eventID: validEventID, quantity: 0, wantErr: domain.ErrInvalidQuantity},
		{name: "Negative quantity rejected", orderID: validOrderID, userID: 1, eventID: validEventID, quantity: -5, wantErr: domain.ErrInvalidQuantity},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.NewOrder(tt.orderID, tt.userID, tt.eventID, tt.quantity)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, domain.Order{}, got, "invalid input must return zero Order")
				return
			}

			assert.NoError(t, err)
			if tt.assertSet {
				assert.Equal(t, tt.orderID, got.ID(), "ID is caller-supplied, must round-trip")
				assert.Equal(t, tt.userID, got.UserID())
				assert.Equal(t, tt.eventID, got.EventID())
				assert.Equal(t, tt.quantity, got.Quantity())
				assert.Equal(t, domain.OrderStatusPending, got.Status(), "new orders must start pending")
				assert.False(t, got.CreatedAt().IsZero(), "CreatedAt is factory-assigned, must not be zero")
			}
		})
	}
}

// newTestOrder is a convenience helper for tests that don't care about
// the specific UUID — they just need a valid Order. Centralised so a
// future factory-shape change doesn't ripple across every transition
// test below. Uses require.NoError so a factory-side regression
// produces a clear failure here instead of a confusing nil-Order
// downstream assertion.
func newTestOrder(t *testing.T, userID int, eventID uuid.UUID, quantity int) domain.Order {
	t.Helper()
	o, err := domain.NewOrder(uuid.New(), userID, eventID, quantity)
	require.NoError(t, err)
	return o
}

// TestNewReservation exercises the Pattern A factory contract: same
// invariants as NewOrder PLUS reservedUntil must be a non-zero future
// time. Returns AwaitingPayment status (NOT Pending) — that's the
// load-bearing difference between the two factories.
func TestNewReservation(t *testing.T) {
	t.Parallel()

	validOrderID := uuid.New()
	validEventID := uuid.New()
	futureTTL := time.Now().Add(15 * time.Minute)

	tests := []struct {
		name          string
		orderID       uuid.UUID
		userID        int
		eventID       uuid.UUID
		quantity      int
		reservedUntil time.Time
		wantErr       error
	}{
		{name: "Valid 15-min reservation", orderID: validOrderID, userID: 1, eventID: validEventID, quantity: 1, reservedUntil: futureTTL},
		{name: "Valid 1-hour reservation", orderID: uuid.New(), userID: 42, eventID: uuid.New(), quantity: 3, reservedUntil: time.Now().Add(time.Hour)},
		// Same invariants as NewOrder.
		{name: "Zero orderID rejected", orderID: uuid.Nil, userID: 1, eventID: validEventID, quantity: 1, reservedUntil: futureTTL, wantErr: domain.ErrInvalidOrderID},
		{name: "Zero userID rejected", orderID: validOrderID, userID: 0, eventID: validEventID, quantity: 1, reservedUntil: futureTTL, wantErr: domain.ErrInvalidUserID},
		{name: "Zero eventID rejected", orderID: validOrderID, userID: 1, eventID: uuid.Nil, quantity: 1, reservedUntil: futureTTL, wantErr: domain.ErrInvalidEventID},
		{name: "Zero quantity rejected", orderID: validOrderID, userID: 1, eventID: validEventID, quantity: 0, reservedUntil: futureTTL, wantErr: domain.ErrInvalidQuantity},
		// Pattern A-specific invariants.
		{name: "Zero reservedUntil rejected", orderID: validOrderID, userID: 1, eventID: validEventID, quantity: 1, reservedUntil: time.Time{}, wantErr: domain.ErrInvalidReservedUntil},
		{name: "Past reservedUntil rejected", orderID: validOrderID, userID: 1, eventID: validEventID, quantity: 1, reservedUntil: time.Now().Add(-1 * time.Minute), wantErr: domain.ErrInvalidReservedUntil},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.NewReservation(tt.orderID, tt.userID, tt.eventID, tt.quantity, tt.reservedUntil)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, domain.Order{}, got, "invalid input must return zero Order")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.orderID, got.ID())
			assert.Equal(t, tt.userID, got.UserID())
			assert.Equal(t, tt.eventID, got.EventID())
			assert.Equal(t, tt.quantity, got.Quantity())
			assert.Equal(t, domain.OrderStatusAwaitingPayment, got.Status(),
				"NewReservation must produce AwaitingPayment, NOT Pending — that's what makes it Pattern A")
			assert.Equal(t, tt.reservedUntil, got.ReservedUntil())
			assert.False(t, got.CreatedAt().IsZero(), "CreatedAt is factory-assigned")
		})
	}
}

// TestNewReservation_IsMalformedOrderInput_Classification: the worker's
// DLQ classifier branches on `domain.IsMalformedOrderInput` to skip
// the retry budget for deterministic-failure messages. The new
// `ErrInvalidReservedUntil` sentinel must be classified the same way
// as the existing invariants — otherwise a clock-skewed message
// burns 3× retries before going to DLQ.
func TestNewReservation_IsMalformedOrderInput_Classification(t *testing.T) {
	t.Parallel()

	_, err := domain.NewReservation(uuid.New(), 1, uuid.New(), 1, time.Time{})
	require.Error(t, err)
	assert.True(t, domain.IsMalformedOrderInput(err),
		"ErrInvalidReservedUntil must be classified as malformed input so the worker DLQ classifier short-circuits the retry budget")
}

// TestOrder_Transitions_HappyPath verifies the three legal state-
// machine edges. Each method takes a value receiver and returns a new
// Order — the receiver MUST be untouched (immutable transition) so
// concurrent reads of the same Order value are safe.
//
// Transition graph (also documented on ErrInvalidTransition):
//
//   Pending ──MarkConfirmed──→  Confirmed   (terminal)
//   Pending ──MarkFailed─────→  Failed
//   Failed  ──MarkCompensated→  Compensated (terminal)
func TestOrder_Transitions_HappyPath(t *testing.T) {
	t.Parallel()

	t.Run("MarkConfirmed: Pending → Confirmed", func(t *testing.T) {
		t.Parallel()
		original := newTestOrder(t, 1, uuid.New(), 1)
		confirmed, err := original.MarkConfirmed()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusConfirmed, confirmed.Status())
		assert.Equal(t, domain.OrderStatusPending, original.Status(), "receiver must be untouched")
		// Identity + non-status fields preserved.
		assert.Equal(t, original.ID(), confirmed.ID())
		assert.Equal(t, original.UserID(), confirmed.UserID())
		assert.Equal(t, original.EventID(), confirmed.EventID())
		assert.Equal(t, original.Quantity(), confirmed.Quantity())
	})

	t.Run("MarkFailed: Pending → Failed", func(t *testing.T) {
		t.Parallel()
		original := newTestOrder(t, 1, uuid.New(), 1)
		failed, err := original.MarkFailed()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusFailed, failed.Status())
		assert.Equal(t, domain.OrderStatusPending, original.Status(), "receiver must be untouched")
	})

	t.Run("MarkCompensated: Failed → Compensated", func(t *testing.T) {
		t.Parallel()
		// Build a Failed order via the legal path Pending→Failed.
		o := newTestOrder(t, 1, uuid.New(), 1)
		failed, err := o.MarkFailed()
		assert.NoError(t, err)
		compensated, err := failed.MarkCompensated()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusCompensated, compensated.Status())
		assert.Equal(t, domain.OrderStatusFailed, failed.Status(), "receiver must be untouched")
	})

	// A4 — new transitions through the Charging intent state.

	t.Run("A4 MarkCharging: Pending → Charging", func(t *testing.T) {
		t.Parallel()
		original := newTestOrder(t, 1, uuid.New(), 1)
		charging, err := original.MarkCharging()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusCharging, charging.Status())
		assert.Equal(t, domain.OrderStatusPending, original.Status(), "receiver must be untouched")
	})

	t.Run("A4 MarkConfirmed: Charging → Confirmed (new canonical path)", func(t *testing.T) {
		t.Parallel()
		o := newTestOrder(t, 1, uuid.New(), 1)
		charging, err := o.MarkCharging()
		assert.NoError(t, err)
		confirmed, err := charging.MarkConfirmed()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusConfirmed, confirmed.Status())
	})

	t.Run("A4 MarkFailed: Charging → Failed (new canonical path)", func(t *testing.T) {
		t.Parallel()
		o := newTestOrder(t, 1, uuid.New(), 1)
		charging, err := o.MarkCharging()
		assert.NoError(t, err)
		failed, err := charging.MarkFailed()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusFailed, failed.Status())
	})

	// Pattern A — D2 transitions through reservation + payment states.

	t.Run("D2 MarkAwaitingPayment: Pending → AwaitingPayment", func(t *testing.T) {
		t.Parallel()
		original := newTestOrder(t, 1, uuid.New(), 1)
		awaiting, err := original.MarkAwaitingPayment()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusAwaitingPayment, awaiting.Status())
		assert.Equal(t, domain.OrderStatusPending, original.Status(), "receiver must be untouched")
	})

	t.Run("D2 MarkPaid: AwaitingPayment → Paid", func(t *testing.T) {
		t.Parallel()
		o := newTestOrder(t, 1, uuid.New(), 1)
		awaiting, err := o.MarkAwaitingPayment()
		assert.NoError(t, err)
		paid, err := awaiting.MarkPaid()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusPaid, paid.Status())
		assert.Equal(t, domain.OrderStatusAwaitingPayment, awaiting.Status(), "receiver must be untouched")
	})

	t.Run("D2 MarkExpired: AwaitingPayment → Expired", func(t *testing.T) {
		t.Parallel()
		o := newTestOrder(t, 1, uuid.New(), 1)
		awaiting, err := o.MarkAwaitingPayment()
		assert.NoError(t, err)
		expired, err := awaiting.MarkExpired()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusExpired, expired.Status())
	})

	t.Run("D2 MarkPaymentFailed: AwaitingPayment → PaymentFailed", func(t *testing.T) {
		t.Parallel()
		o := newTestOrder(t, 1, uuid.New(), 1)
		awaiting, err := o.MarkAwaitingPayment()
		assert.NoError(t, err)
		paymentFailed, err := awaiting.MarkPaymentFailed()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusPaymentFailed, paymentFailed.Status())
	})

	t.Run("D2 MarkCompensated: Expired → Compensated", func(t *testing.T) {
		t.Parallel()
		o := newTestOrder(t, 1, uuid.New(), 1)
		awaiting, err := o.MarkAwaitingPayment()
		assert.NoError(t, err)
		expired, err := awaiting.MarkExpired()
		assert.NoError(t, err)
		compensated, err := expired.MarkCompensated()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusCompensated, compensated.Status())
	})

	t.Run("D2 MarkCompensated: PaymentFailed → Compensated", func(t *testing.T) {
		t.Parallel()
		o := newTestOrder(t, 1, uuid.New(), 1)
		awaiting, err := o.MarkAwaitingPayment()
		assert.NoError(t, err)
		paymentFailed, err := awaiting.MarkPaymentFailed()
		assert.NoError(t, err)
		compensated, err := paymentFailed.MarkCompensated()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusCompensated, compensated.Status())
	})
}

// TestOrder_Transitions_IllegalSource verifies every illegal edge in
// the state graph rejects with `ErrInvalidTransition`. Without these
// guards, a concurrent compensation race or a logic bug could silently
// downgrade an already-Confirmed order to Failed (or any other
// illegal move). The matrix is intentionally exhaustive — adding a
// new state should also add the corresponding row here.
func TestOrder_Transitions_IllegalSource(t *testing.T) {
	t.Parallel()

	type tc struct {
		name      string
		from      domain.OrderStatus
		do        func(o domain.Order) (domain.Order, error)
		expectErr error
	}
	tests := []tc{
		// MarkCharging: only Pending is legal (A4).
		{"MarkCharging from Charging", domain.OrderStatusCharging, domain.Order.MarkCharging, domain.ErrInvalidTransition},
		{"MarkCharging from Confirmed", domain.OrderStatusConfirmed, domain.Order.MarkCharging, domain.ErrInvalidTransition},
		{"MarkCharging from Failed", domain.OrderStatusFailed, domain.Order.MarkCharging, domain.ErrInvalidTransition},
		{"MarkCharging from Compensated", domain.OrderStatusCompensated, domain.Order.MarkCharging, domain.ErrInvalidTransition},
		// MarkConfirmed: Pending OR Charging legal (A4 transitional widening).
		// Confirmed/Failed/Compensated all illegal.
		{"MarkConfirmed from Confirmed", domain.OrderStatusConfirmed, domain.Order.MarkConfirmed, domain.ErrInvalidTransition},
		{"MarkConfirmed from Failed", domain.OrderStatusFailed, domain.Order.MarkConfirmed, domain.ErrInvalidTransition},
		{"MarkConfirmed from Compensated", domain.OrderStatusCompensated, domain.Order.MarkConfirmed, domain.ErrInvalidTransition},
		// MarkFailed: Pending OR Charging legal (A4 transitional widening).
		{"MarkFailed from Confirmed", domain.OrderStatusConfirmed, domain.Order.MarkFailed, domain.ErrInvalidTransition},
		{"MarkFailed from Failed", domain.OrderStatusFailed, domain.Order.MarkFailed, domain.ErrInvalidTransition},
		{"MarkFailed from Compensated", domain.OrderStatusCompensated, domain.Order.MarkFailed, domain.ErrInvalidTransition},
		// MarkCompensated: Failed | Expired | PaymentFailed legal (D2 widening).
		// All other source states are illegal.
		{"MarkCompensated from Pending", domain.OrderStatusPending, domain.Order.MarkCompensated, domain.ErrInvalidTransition},
		{"MarkCompensated from Charging", domain.OrderStatusCharging, domain.Order.MarkCompensated, domain.ErrInvalidTransition},
		{"MarkCompensated from Confirmed", domain.OrderStatusConfirmed, domain.Order.MarkCompensated, domain.ErrInvalidTransition},
		{"MarkCompensated from Compensated", domain.OrderStatusCompensated, domain.Order.MarkCompensated, domain.ErrInvalidTransition},
		{"MarkCompensated from AwaitingPayment", domain.OrderStatusAwaitingPayment, domain.Order.MarkCompensated, domain.ErrInvalidTransition},
		{"MarkCompensated from Paid", domain.OrderStatusPaid, domain.Order.MarkCompensated, domain.ErrInvalidTransition},

		// D2 — Pattern A transitions. Each only accepts a narrow source.
		// MarkAwaitingPayment: only Pending is legal. Every other source
		// state must be illegal — including the failure-terminal trio
		// (Failed | Compensated | PaymentFailed) so a watchdog or recon
		// re-drive can never accidentally walk a terminal order back into
		// the reservation flow.
		{"MarkAwaitingPayment from Charging", domain.OrderStatusCharging, domain.Order.MarkAwaitingPayment, domain.ErrInvalidTransition},
		{"MarkAwaitingPayment from AwaitingPayment", domain.OrderStatusAwaitingPayment, domain.Order.MarkAwaitingPayment, domain.ErrInvalidTransition},
		{"MarkAwaitingPayment from Confirmed", domain.OrderStatusConfirmed, domain.Order.MarkAwaitingPayment, domain.ErrInvalidTransition},
		{"MarkAwaitingPayment from Paid", domain.OrderStatusPaid, domain.Order.MarkAwaitingPayment, domain.ErrInvalidTransition},
		{"MarkAwaitingPayment from Expired", domain.OrderStatusExpired, domain.Order.MarkAwaitingPayment, domain.ErrInvalidTransition},
		{"MarkAwaitingPayment from Failed", domain.OrderStatusFailed, domain.Order.MarkAwaitingPayment, domain.ErrInvalidTransition},
		{"MarkAwaitingPayment from Compensated", domain.OrderStatusCompensated, domain.Order.MarkAwaitingPayment, domain.ErrInvalidTransition},
		{"MarkAwaitingPayment from PaymentFailed", domain.OrderStatusPaymentFailed, domain.Order.MarkAwaitingPayment, domain.ErrInvalidTransition},
		// MarkPaid: only AwaitingPayment is legal.
		{"MarkPaid from Pending", domain.OrderStatusPending, domain.Order.MarkPaid, domain.ErrInvalidTransition},
		{"MarkPaid from Charging", domain.OrderStatusCharging, domain.Order.MarkPaid, domain.ErrInvalidTransition},
		{"MarkPaid from Paid", domain.OrderStatusPaid, domain.Order.MarkPaid, domain.ErrInvalidTransition},
		{"MarkPaid from Expired", domain.OrderStatusExpired, domain.Order.MarkPaid, domain.ErrInvalidTransition},
		{"MarkPaid from PaymentFailed", domain.OrderStatusPaymentFailed, domain.Order.MarkPaid, domain.ErrInvalidTransition},
		// MarkExpired: only AwaitingPayment is legal.
		{"MarkExpired from Pending", domain.OrderStatusPending, domain.Order.MarkExpired, domain.ErrInvalidTransition},
		{"MarkExpired from Paid", domain.OrderStatusPaid, domain.Order.MarkExpired, domain.ErrInvalidTransition},
		{"MarkExpired from Expired", domain.OrderStatusExpired, domain.Order.MarkExpired, domain.ErrInvalidTransition},
		{"MarkExpired from PaymentFailed", domain.OrderStatusPaymentFailed, domain.Order.MarkExpired, domain.ErrInvalidTransition},
		// MarkPaymentFailed: only AwaitingPayment is legal.
		{"MarkPaymentFailed from Pending", domain.OrderStatusPending, domain.Order.MarkPaymentFailed, domain.ErrInvalidTransition},
		{"MarkPaymentFailed from Paid", domain.OrderStatusPaid, domain.Order.MarkPaymentFailed, domain.ErrInvalidTransition},
		{"MarkPaymentFailed from Expired", domain.OrderStatusExpired, domain.Order.MarkPaymentFailed, domain.ErrInvalidTransition},
		{"MarkPaymentFailed from PaymentFailed", domain.OrderStatusPaymentFailed, domain.Order.MarkPaymentFailed, domain.ErrInvalidTransition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Reconstruct an order with the test's source status.
			id := uuid.New()
			eventID := uuid.New()
			o := domain.ReconstructOrder(id, 1, eventID, 1, tt.from, time.Now(), time.Time{}, "")
			got, err := tt.do(o)
			assert.ErrorIs(t, err, tt.expectErr)
			// The receiver-side guarantee: failed transitions return a
			// zero-value Order, NOT a partially-mutated one.
			assert.Equal(t, domain.OrderStatus(""), got.Status(),
				"failed transition must return zero-value Order")
		})
	}
}

func TestReconstructOrder_BypassesInvariants(t *testing.T) {
	t.Parallel()

	// Reconstruction must accept ANY persisted state, including values
	// that NewOrder would reject. The repository is the source of truth
	// for rehydration; invariants only apply at create-time.
	id := uuid.New()
	eventID := uuid.New()
	created := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	got := domain.ReconstructOrder(id, 1, eventID, 0, domain.OrderStatusFailed, created, time.Time{}, "")

	assert.Equal(t, id, got.ID())
	assert.Equal(t, 0, got.Quantity(), "Reconstruct should not validate quantity")
	assert.Equal(t, domain.OrderStatusFailed, got.Status())
	assert.Equal(t, created, got.CreatedAt())
}

func TestNewOrder_ErrorClassesDistinct(t *testing.T) {
	t.Parallel()

	// Sanity check that callers can distinguish error classes via
	// errors.Is — important because the worker DLQ classification
	// will branch on this.
	validOrderID := uuid.New()
	validEventID := uuid.New()
	_, errOrderID := domain.NewOrder(uuid.Nil, 1, validEventID, 1)
	_, errUser := domain.NewOrder(validOrderID, 0, validEventID, 1)
	_, errEvent := domain.NewOrder(validOrderID, 1, uuid.Nil, 1)
	_, errQty := domain.NewOrder(validOrderID, 1, validEventID, 0)

	assert.True(t, errors.Is(errOrderID, domain.ErrInvalidOrderID))
	assert.True(t, errors.Is(errUser, domain.ErrInvalidUserID))
	assert.False(t, errors.Is(errUser, domain.ErrInvalidEventID))
	assert.True(t, errors.Is(errEvent, domain.ErrInvalidEventID))
	assert.True(t, errors.Is(errQty, domain.ErrInvalidQuantity))
}

// TestOrderStatus_IsValid_Exhaustiveness asserts that every OrderStatus
// constant exported from the domain package is recognised by IsValid.
// Go has no compile-time exhaustiveness check for typed strings, so a
// new constant added without updating IsValid would silently fail open
// — StatusFilter() (api/dto layer) maps unrecognised statuses to "no
// filter, return everything", which has historically been a footgun
// (see PR #75 dto regression where adding "expired" to the test
// sentinel masked the new Pattern A status as the unknown).
//
// This test enumerates the canonical list explicitly so adding a new
// status WITHOUT also adding it to IsValid + this test produces a
// failing test diff at code-review time. The test fails LOUDLY (assert
// per-status) so reviewers see exactly which status was overlooked.
func TestOrderStatus_IsValid_Exhaustiveness(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status domain.OrderStatus
	}{
		// Legacy / A4 set.
		{"Confirmed", domain.OrderStatusConfirmed},
		{"Pending", domain.OrderStatusPending},
		{"Charging", domain.OrderStatusCharging},
		{"Failed", domain.OrderStatusFailed},
		// Pattern A set (D2).
		{"AwaitingPayment", domain.OrderStatusAwaitingPayment},
		{"Paid", domain.OrderStatusPaid},
		{"Expired", domain.OrderStatusExpired},
		{"PaymentFailed", domain.OrderStatusPaymentFailed},
		// Shared terminal.
		{"Compensated", domain.OrderStatusCompensated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, tc.status.IsValid(),
				"OrderStatus(%q) must be recognised by IsValid — adding a new status without updating IsValid is a footgun (StatusFilter would silently degrade to 'no filter')", tc.status)
		})
	}

	// Negative companion: a string that looks like a status but isn't
	// declared as a constant must NOT be IsValid. Catches the case
	// where IsValid drifts to `return true` (always-pass).
	t.Run("unknown status rejected", func(t *testing.T) {
		t.Parallel()
		assert.False(t, domain.OrderStatus("definitely_not_a_real_status").IsValid(),
			"IsValid must reject strings that aren't declared OrderStatus constants")
		assert.False(t, domain.OrderStatus("").IsValid(),
			"IsValid must reject empty string")
	})
}
