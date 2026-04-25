package domain_test

import (
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/domain"

	"github.com/stretchr/testify/assert"
)

func TestNewOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		userID    int
		eventID   int
		quantity  int
		wantErr   error
		assertSet bool
	}{
		{name: "Valid", userID: 1, eventID: 1, quantity: 1, assertSet: true},
		{name: "Valid larger qty", userID: 42, eventID: 7, quantity: 3, assertSet: true},
		{name: "Zero userID rejected", userID: 0, eventID: 1, quantity: 1, wantErr: domain.ErrInvalidUserID},
		{name: "Negative userID rejected", userID: -1, eventID: 1, quantity: 1, wantErr: domain.ErrInvalidUserID},
		{name: "Zero eventID rejected", userID: 1, eventID: 0, quantity: 1, wantErr: domain.ErrInvalidEventID},
		{name: "Zero quantity rejected", userID: 1, eventID: 1, quantity: 0, wantErr: domain.ErrInvalidQuantity},
		{name: "Negative quantity rejected", userID: 1, eventID: 1, quantity: -5, wantErr: domain.ErrInvalidQuantity},
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
				assert.Equal(t, tt.userID, got.UserID)
				assert.Equal(t, tt.eventID, got.EventID)
				assert.Equal(t, tt.quantity, got.Quantity)
				assert.Equal(t, domain.OrderStatusPending, got.Status, "new orders must start pending")
				assert.Equal(t, 0, got.ID, "ID is repo-assigned, must be zero at construction")
				assert.True(t, got.CreatedAt.IsZero(), "CreatedAt is repo-assigned, must be zero at construction")
			}
		})
	}
}

func TestOrder_WithStatus_Immutable(t *testing.T) {
	t.Parallel()

	original, err := domain.NewOrder(1, 1, 1)
	assert.NoError(t, err)
	assert.Equal(t, domain.OrderStatusPending, original.Status)

	confirmed := original.WithStatus(domain.OrderStatusConfirmed)

	// Original receiver MUST be untouched (immutable transition).
	assert.Equal(t, domain.OrderStatusPending, original.Status,
		"WithStatus must not mutate the receiver")
	// Returned copy carries the new status.
	assert.Equal(t, domain.OrderStatusConfirmed, confirmed.Status)
	// All other fields preserved.
	assert.Equal(t, original.UserID, confirmed.UserID)
	assert.Equal(t, original.EventID, confirmed.EventID)
	assert.Equal(t, original.Quantity, confirmed.Quantity)
}

func TestReconstructOrder_BypassesInvariants(t *testing.T) {
	t.Parallel()

	// Reconstruction must accept ANY persisted state, including values
	// that NewOrder would reject (e.g. quantity = 0 if a future schema
	// change ever allows it). The repository is the source of truth for
	// rehydration; invariants only apply at create-time.
	created := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	got := domain.ReconstructOrder(99, 1, 1, 0, domain.OrderStatusFailed, created)

	assert.Equal(t, 99, got.ID)
	assert.Equal(t, 0, got.Quantity, "Reconstruct should not validate quantity")
	assert.Equal(t, domain.OrderStatusFailed, got.Status)
	assert.Equal(t, created, got.CreatedAt)
}

func TestNewOrder_ErrorClassesDistinct(t *testing.T) {
	t.Parallel()

	// Sanity check that callers can distinguish error classes via
	// errors.Is — important because the worker DLQ classification
	// will branch on this.
	_, errUser := domain.NewOrder(0, 1, 1)
	_, errEvent := domain.NewOrder(1, 0, 1)
	_, errQty := domain.NewOrder(1, 1, 0)

	assert.True(t, errors.Is(errUser, domain.ErrInvalidUserID))
	assert.False(t, errors.Is(errUser, domain.ErrInvalidEventID))
	assert.True(t, errors.Is(errEvent, domain.ErrInvalidEventID))
	assert.True(t, errors.Is(errQty, domain.ErrInvalidQuantity))
}
