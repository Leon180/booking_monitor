package domain_test

import (
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/domain"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNewOrder(t *testing.T) {
	t.Parallel()

	validEventID := uuid.New()

	tests := []struct {
		name      string
		userID    int
		eventID   uuid.UUID
		quantity  int
		wantErr   error
		assertSet bool
	}{
		{name: "Valid", userID: 1, eventID: validEventID, quantity: 1, assertSet: true},
		{name: "Valid larger qty", userID: 42, eventID: uuid.New(), quantity: 3, assertSet: true},
		{name: "Zero userID rejected", userID: 0, eventID: validEventID, quantity: 1, wantErr: domain.ErrInvalidUserID},
		{name: "Negative userID rejected", userID: -1, eventID: validEventID, quantity: 1, wantErr: domain.ErrInvalidUserID},
		{name: "Zero eventID (uuid.Nil) rejected", userID: 1, eventID: uuid.Nil, quantity: 1, wantErr: domain.ErrInvalidEventID},
		{name: "Zero quantity rejected", userID: 1, eventID: validEventID, quantity: 0, wantErr: domain.ErrInvalidQuantity},
		{name: "Negative quantity rejected", userID: 1, eventID: validEventID, quantity: -5, wantErr: domain.ErrInvalidQuantity},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.NewOrder(tt.userID, tt.eventID, tt.quantity)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, domain.Order{}, got, "invalid input must return zero Order")
				return
			}

			assert.NoError(t, err)
			if tt.assertSet {
				assert.Equal(t, tt.userID, got.UserID())
				assert.Equal(t, tt.eventID, got.EventID())
				assert.Equal(t, tt.quantity, got.Quantity())
				assert.Equal(t, domain.OrderStatusPending, got.Status(), "new orders must start pending")
				assert.NotEqual(t, uuid.Nil, got.ID(), "ID is factory-generated UUID v7, must not be zero")
				assert.False(t, got.CreatedAt().IsZero(), "CreatedAt is factory-assigned, must not be zero")
			}
		})
	}
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
		original, err := domain.NewOrder(1, uuid.New(), 1)
		assert.NoError(t, err)
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
		original, err := domain.NewOrder(1, uuid.New(), 1)
		assert.NoError(t, err)
		failed, err := original.MarkFailed()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusFailed, failed.Status())
		assert.Equal(t, domain.OrderStatusPending, original.Status(), "receiver must be untouched")
	})

	t.Run("MarkCompensated: Failed → Compensated", func(t *testing.T) {
		t.Parallel()
		// Build a Failed order via the legal path Pending→Failed.
		o, err := domain.NewOrder(1, uuid.New(), 1)
		assert.NoError(t, err)
		failed, err := o.MarkFailed()
		assert.NoError(t, err)
		compensated, err := failed.MarkCompensated()
		assert.NoError(t, err)
		assert.Equal(t, domain.OrderStatusCompensated, compensated.Status())
		assert.Equal(t, domain.OrderStatusFailed, failed.Status(), "receiver must be untouched")
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
		// MarkConfirmed: only Pending is legal.
		{"MarkConfirmed from Confirmed", domain.OrderStatusConfirmed, domain.Order.MarkConfirmed, domain.ErrInvalidTransition},
		{"MarkConfirmed from Failed", domain.OrderStatusFailed, domain.Order.MarkConfirmed, domain.ErrInvalidTransition},
		{"MarkConfirmed from Compensated", domain.OrderStatusCompensated, domain.Order.MarkConfirmed, domain.ErrInvalidTransition},
		// MarkFailed: only Pending is legal.
		{"MarkFailed from Confirmed", domain.OrderStatusConfirmed, domain.Order.MarkFailed, domain.ErrInvalidTransition},
		{"MarkFailed from Failed", domain.OrderStatusFailed, domain.Order.MarkFailed, domain.ErrInvalidTransition},
		{"MarkFailed from Compensated", domain.OrderStatusCompensated, domain.Order.MarkFailed, domain.ErrInvalidTransition},
		// MarkCompensated: only Failed is legal.
		{"MarkCompensated from Pending", domain.OrderStatusPending, domain.Order.MarkCompensated, domain.ErrInvalidTransition},
		{"MarkCompensated from Confirmed", domain.OrderStatusConfirmed, domain.Order.MarkCompensated, domain.ErrInvalidTransition},
		{"MarkCompensated from Compensated", domain.OrderStatusCompensated, domain.Order.MarkCompensated, domain.ErrInvalidTransition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Reconstruct an order with the test's source status.
			id := uuid.New()
			eventID := uuid.New()
			o := domain.ReconstructOrder(id, 1, eventID, 1, tt.from, time.Now())
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
	got := domain.ReconstructOrder(id, 1, eventID, 0, domain.OrderStatusFailed, created)

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
	validEventID := uuid.New()
	_, errUser := domain.NewOrder(0, validEventID, 1)
	_, errEvent := domain.NewOrder(1, uuid.Nil, 1)
	_, errQty := domain.NewOrder(1, validEventID, 0)

	assert.True(t, errors.Is(errUser, domain.ErrInvalidUserID))
	assert.False(t, errors.Is(errUser, domain.ErrInvalidEventID))
	assert.True(t, errors.Is(errEvent, domain.ErrInvalidEventID))
	assert.True(t, errors.Is(errQty, domain.ErrInvalidQuantity))
}
