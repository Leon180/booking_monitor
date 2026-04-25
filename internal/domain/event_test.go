package domain_test

import (
	"encoding/json"
	"testing"

	"booking_monitor/internal/domain"

	"github.com/stretchr/testify/assert"
)

func TestNewOrderCreatedOutbox(t *testing.T) {
	t.Parallel()
	payload, _ := json.Marshal(map[string]int{"order_id": 1})

	got := domain.NewOrderCreatedOutbox(payload)

	assert.Equal(t, domain.EventTypeOrderCreated, got.EventType)
	assert.Equal(t, domain.OutboxStatusPending, got.Status)
	assert.Equal(t, payload, got.Payload)
	assert.Equal(t, 0, got.ID, "ID is repo-assigned")
	assert.Nil(t, got.ProcessedAt)
}

func TestNewOrderFailedOutbox(t *testing.T) {
	t.Parallel()
	payload, _ := json.Marshal(map[string]int{"order_id": 1})

	got := domain.NewOrderFailedOutbox(payload)

	assert.Equal(t, domain.EventTypeOrderFailed, got.EventType)
	assert.Equal(t, domain.OutboxStatusPending, got.Status)
	assert.Equal(t, payload, got.Payload)
	assert.Equal(t, 0, got.ID)
	assert.Nil(t, got.ProcessedAt)
}

// TestEventTypeConstantsAreStable pins the wire format of the outbox
// event-type constants. The factories above use these constants, so
// renaming the const to a different string would silently break every
// downstream consumer (Kafka subscribers / saga compensator). Keep
// this test passing or coordinate the rename across all consumers.
func TestEventTypeConstantsAreStable(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "order.created", domain.EventTypeOrderCreated)
	assert.Equal(t, "order.failed", domain.EventTypeOrderFailed)
	assert.Equal(t, "PENDING", domain.OutboxStatusPending)
}

func TestEvent_Deduct(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		initialTickets int
		deductAmount   int
		expectedError  error
		expectedRemain int
	}{
		{
			name:           "Success",
			initialTickets: 10,
			deductAmount:   2,
			expectedError:  nil,
			expectedRemain: 8,
		},
		{
			name:           "Exact Amount",
			initialTickets: 2,
			deductAmount:   2,
			expectedError:  nil,
			expectedRemain: 0,
		},
		{
			name:           "Sold Out",
			initialTickets: 1,
			deductAmount:   2,
			expectedError:  domain.ErrSoldOut,
			expectedRemain: 1, // Should not change
		},
		{
			name:           "Invalid Quantity",
			initialTickets: 10,
			deductAmount:   -1,
			expectedError:  assert.AnError, // We just check error presence, or specific error if exported
			expectedRemain: 10,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			event := &domain.Event{AvailableTickets: tt.initialTickets}
			next, err := event.Deduct(tt.deductAmount)

			if tt.expectedError != nil {
				if tt.expectedError == assert.AnError {
					assert.Error(t, err)
				} else {
					assert.Equal(t, tt.expectedError, err)
				}
				// Original event must be untouched on failure.
				assert.Equal(t, tt.initialTickets, event.AvailableTickets)
				assert.Nil(t, next)
				return
			}

			assert.NoError(t, err)
			// Original receiver is never mutated (immutable pattern).
			assert.Equal(t, tt.initialTickets, event.AvailableTickets)
			// Returned *Event reflects the deducted state.
			assert.NotNil(t, next)
			assert.Equal(t, tt.expectedRemain, next.AvailableTickets)
		})
	}
}
