package dto_test

import (
	"testing"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestOrderResponseFromDomain(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	orderID := uuid.New()
	eventID := uuid.New()
	got := dto.OrderResponseFromDomain(domain.ReconstructOrder(
		orderID, 7, eventID, 3, domain.OrderStatusConfirmed, created,
	))

	// Field-by-field — a swap (e.g. UserID/EventID) silently breaks
	// the wire contract for every consumer; this test is what stops it.
	assert.Equal(t, orderID, got.ID)
	assert.Equal(t, eventID, got.EventID, "EventID must come from domain.Event field, not UserID")
	assert.Equal(t, 7, got.UserID, "UserID must come from domain.UserID field, not EventID")
	assert.Equal(t, 3, got.Quantity)
	assert.Equal(t, "confirmed", got.Status, "Status flattens domain.OrderStatus enum to plain string")
	assert.Equal(t, created, got.CreatedAt)
}

func TestEventResponseFromDomain(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	got := dto.EventResponseFromDomain(domain.ReconstructEvent(
		id, "Concert", 100, 42, 5,
	))

	assert.Equal(t, id, got.ID)
	assert.Equal(t, "Concert", got.Name)
	assert.Equal(t, 100, got.TotalTickets)
	assert.Equal(t, 42, got.AvailableTickets, "AvailableTickets and TotalTickets must not be swapped")
	assert.Equal(t, 5, got.Version)
}

func TestListBookingsResponseFromDomain(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	id1 := uuid.New()
	id2 := uuid.New()
	orders := []domain.Order{
		domain.ReconstructOrder(id1, 7, uuid.New(), 1, domain.OrderStatusPending, now),
		domain.ReconstructOrder(id2, 8, uuid.New(), 5, domain.OrderStatusConfirmed, now),
	}

	got := dto.ListBookingsResponseFromDomain(orders, 17, 2, 10)

	// Meta block
	assert.Equal(t, 17, got.Meta.Total)
	assert.Equal(t, 2, got.Meta.Page)
	assert.Equal(t, 10, got.Meta.Size)

	// Data block — assembled in input order, type converted via mapper
	assert.Len(t, got.Data, 2)
	assert.Equal(t, id1, got.Data[0].ID)
	assert.Equal(t, "pending", got.Data[0].Status)
	assert.Equal(t, id2, got.Data[1].ID)
	assert.Equal(t, "confirmed", got.Data[1].Status)
}

// TestListBookingsResponseFromDomain_EmptySlice ensures the response
// shape is stable when the page is empty — the JSON contract uses an
// empty array, not a missing field, and Make(...) inside the mapper
// is what guarantees that (vs nil-slice marshal as "null" in JSON).
func TestListBookingsResponseFromDomain_EmptySlice(t *testing.T) {
	t.Parallel()

	got := dto.ListBookingsResponseFromDomain(nil, 0, 1, 10)

	assert.NotNil(t, got.Data, "empty Data must be [], not null, in JSON")
	assert.Empty(t, got.Data)
	assert.Equal(t, 0, got.Meta.Total)
}

func TestListBookingsQueryParams_StatusFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      *string
		expected *domain.OrderStatus
	}{
		{name: "Nil status returns nil filter", raw: nil, expected: nil},
		{name: "Pending filter", raw: ptr("pending"), expected: orderStatusPtr(domain.OrderStatusPending)},
		{name: "Confirmed filter", raw: ptr("confirmed"), expected: orderStatusPtr(domain.OrderStatusConfirmed)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			params := dto.ListBookingsQueryParams{Status: tt.raw}
			got := params.StatusFilter()
			if tt.expected == nil {
				assert.Nil(t, got)
				return
			}
			assert.NotNil(t, got)
			assert.Equal(t, *tt.expected, *got)
		})
	}
}

func ptr[T any](v T) *T                                       { return &v }
func orderStatusPtr(s domain.OrderStatus) *domain.OrderStatus { return &s }
