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

func TestOrder_WithStatus_Immutable(t *testing.T) {
	t.Parallel()

	original, err := domain.NewOrder(1, uuid.New(), 1)
	assert.NoError(t, err)
	assert.Equal(t, domain.OrderStatusPending, original.Status())

	confirmed := original.WithStatus(domain.OrderStatusConfirmed)

	// Original receiver MUST be untouched (immutable transition).
	assert.Equal(t, domain.OrderStatusPending, original.Status(),
		"WithStatus must not mutate the receiver")
	// Returned copy carries the new status.
	assert.Equal(t, domain.OrderStatusConfirmed, confirmed.Status())
	// All other fields preserved.
	assert.Equal(t, original.UserID(), confirmed.UserID())
	assert.Equal(t, original.EventID(), confirmed.EventID())
	assert.Equal(t, original.Quantity(), confirmed.Quantity())
	assert.Equal(t, original.ID(), confirmed.ID())
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
