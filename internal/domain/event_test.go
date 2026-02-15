package domain_test

import (
	"testing"

	"booking_monitor/internal/domain"

	"github.com/stretchr/testify/assert"
)

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
			err := event.Deduct(tt.deductAmount)

			if tt.expectedError != nil {
				if tt.expectedError == assert.AnError {
					assert.Error(t, err)
				} else {
					assert.Equal(t, tt.expectedError, err)
				}
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedRemain, event.AvailableTickets)
		})
	}
}
