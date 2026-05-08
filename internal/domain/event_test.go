package domain_test

import (
	"encoding/json"
	"testing"

	"booking_monitor/internal/domain"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNewOrderFailedOutbox(t *testing.T) {
	t.Parallel()
	payload, _ := json.Marshal(map[string]int{"order_id": 1})

	got, err := domain.NewOrderFailedOutbox(payload)

	assert.NoError(t, err)
	assert.Equal(t, domain.EventTypeOrderFailed, got.EventType())
	assert.Equal(t, domain.OutboxStatusPending, got.Status())
	assert.Equal(t, payload, got.Payload())
	assert.NotEqual(t, uuid.Nil, got.ID())
	assert.Nil(t, got.ProcessedAt())
}

// TestEventTypeConstantsAreStable pins the wire format of the outbox
// event-type constants. The factory above uses these constants, so
// renaming the const to a different string would silently break every
// downstream consumer (saga compensator). Keep this test passing or
// coordinate the rename across all consumers.
func TestEventTypeConstantsAreStable(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "order.failed", domain.EventTypeOrderFailed)
	assert.Equal(t, "PENDING", domain.OutboxStatusPending)
}

func TestNewEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		eventName    string
		totalTickets int
		wantErr      error
	}{
		{name: "Valid", eventName: "Concert", totalTickets: 100},
		{name: "Trims whitespace then validates", eventName: "  Show  ", totalTickets: 1},
		{name: "Empty name rejected", eventName: "", totalTickets: 100, wantErr: domain.ErrInvalidEventName},
		{name: "Whitespace-only name rejected", eventName: "   ", totalTickets: 100, wantErr: domain.ErrInvalidEventName},
		{name: "Zero total tickets rejected", eventName: "Concert", totalTickets: 0, wantErr: domain.ErrInvalidTotalTickets},
		{name: "Negative total tickets rejected", eventName: "Concert", totalTickets: -5, wantErr: domain.ErrInvalidTotalTickets},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.NewEvent(tt.eventName, tt.totalTickets)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, domain.Event{}, got, "invalid input must return zero Event")
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.eventName, got.Name())
			assert.Equal(t, tt.totalTickets, got.TotalTickets())
			assert.Equal(t, tt.totalTickets, got.AvailableTickets(),
				"new events must start with AvailableTickets == TotalTickets")
			assert.NotEqual(t, uuid.Nil, got.ID(), "ID is factory-generated, must not be zero UUID")
			assert.Equal(t, 0, got.Version(), "Version starts at 0")
		})
	}
}

func TestReconstructEvent_BypassesInvariants(t *testing.T) {
	t.Parallel()

	// ReconstructEvent must accept any persisted state, including
	// values NewEvent would reject. The repository is the source of
	// truth for rehydration; invariants only apply at create-time.
	id := uuid.New()
	got := domain.ReconstructEvent(id, "", 0, 0, 7)

	assert.Equal(t, id, got.ID())
	assert.Equal(t, "", got.Name(), "Reconstruct should not validate name")
	assert.Equal(t, 0, got.TotalTickets(), "Reconstruct should not validate totals")
	assert.Equal(t, 0, got.AvailableTickets())
	assert.Equal(t, 7, got.Version())
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
			expectedError:  assert.AnError, // We just check error presence
			expectedRemain: 10,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Use ReconstructEvent (not NewEvent) because the test
			// drives Deduct against arbitrary AvailableTickets values
			// including boundary cases NewEvent's invariants reject.
			event := domain.ReconstructEvent(uuid.New(), "test event", tt.initialTickets, tt.initialTickets, 0)
			next, err := event.Deduct(tt.deductAmount)

			if tt.expectedError != nil {
				if tt.expectedError == assert.AnError {
					assert.Error(t, err)
				} else {
					assert.Equal(t, tt.expectedError, err)
				}
				// Original event must be untouched on failure.
				assert.Equal(t, tt.initialTickets, event.AvailableTickets())
				assert.Equal(t, domain.Event{}, next, "Deduct must return zero Event on error")
				return
			}

			assert.NoError(t, err)
			// Original receiver is never mutated (immutable pattern).
			assert.Equal(t, tt.initialTickets, event.AvailableTickets())
			assert.Equal(t, tt.expectedRemain, next.AvailableTickets())
		})
	}
}
