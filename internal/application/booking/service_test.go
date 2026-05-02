package booking_test

import (
	"context"
	"errors"
	"testing"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func TestBookingService_BookTicket(t *testing.T) {
	// Silent logger via the package's own Nop helper — avoids pulling
	// in zap internals in tests.
	ctx := mlog.NewContext(context.Background(), mlog.NewNop(), "")

	eventID := uuid.New()

	tests := []struct {
		name          string
		userID        int
		eventID       uuid.UUID
		quantity      int
		mockSetup     func(*mocks.MockOrderRepository, *mocks.MockInventoryRepository)
		expectedError error
	}{
		{
			name:     "Success",
			userID:   1,
			eventID:  eventID,
			quantity: 2,
			mockSetup: func(o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository) {
				// PR 47: BookingService now mints the orderID upfront and
				// passes it through to DeductInventory. Match Any() on
				// the orderID position — we don't pin its value here
				// because the test asserts the service-side return
				// value is non-nil/non-zero separately below.
				i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), eventID, 1, 2).Return(true, 0, nil)
			},
			expectedError: nil,
		},
		{
			name:     "Sold Out (Redis)",
			userID:   1,
			eventID:  eventID,
			quantity: 5,
			mockSetup: func(o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository) {
				// Redis returns false (Sold Out)
				i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), eventID, 1, 5).Return(false, 0, nil)
			},
			expectedError: domain.ErrSoldOut,
		},
		{
			// Duplicate purchase is now enforced by DB UNIQUE constraint (not Redis).
			// At the BookingService level, Redis just returns success — the worker handles the duplicate.
			// This test verifies that a Redis error is propagated correctly.
			name:     "Redis Error",
			userID:   1,
			eventID:  eventID,
			quantity: 1,
			mockSetup: func(o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository) {
				i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), eventID, 1, 1).Return(false, 0, errors.New("connection failed"))
			},
			expectedError: errors.New("redis inventory error: connection failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Setup Mocks
			mockOrderRepo := mocks.NewMockOrderRepository(ctrl)
			mockInventoryRepo := mocks.NewMockInventoryRepository(ctrl)

			if tt.mockSetup != nil {
				tt.mockSetup(mockOrderRepo, mockInventoryRepo)
			}

			// Service
			service := booking.NewService(mockOrderRepo, mockInventoryRepo)

			// Execute
			order, err := service.BookTicket(ctx, tt.userID, tt.eventID, tt.quantity)

			// Assert
			if tt.expectedError != nil {
				if tt.name == "Redis Error" {
					assert.EqualError(t, err, tt.expectedError.Error())
				} else {
					assert.ErrorIs(t, err, tt.expectedError)
				}
				assert.Equal(t, domain.Order{}, order, "error path must return zero Order — no order intent persisted")
			} else {
				assert.NoError(t, err)
				assert.NotEqual(t, uuid.Nil, order.ID(), "success path must return an Order with a minted UUIDv7 id")
				assert.Equal(t, tt.userID, order.UserID(), "Order must carry the validated UserID")
				assert.Equal(t, tt.quantity, order.Quantity(), "Order must carry the validated Quantity")
				assert.Equal(t, domain.OrderStatusPending, order.Status(), "newly-constructed Order must be Pending")
			}
		})
	}
}

// TestBookingService_BookTicket_RejectsInvariantViolations pins the
// CP2.6 alignment: invalid userID / quantity / orderID are rejected
// at the application boundary BEFORE any Redis I/O. Prior shape would
// have succeeded at the API + Redis-deduct steps, then failed at the
// worker's NewOrder, requiring a Redis revert.
func TestBookingService_BookTicket_RejectsInvariantViolations(t *testing.T) {
	ctx := mlog.NewContext(context.Background(), mlog.NewNop(), "")
	eventID := uuid.New()

	cases := []struct {
		name        string
		userID      int
		quantity    int
		expectedErr error
	}{
		{name: "userID=0 rejected", userID: 0, quantity: 1, expectedErr: domain.ErrInvalidUserID},
		{name: "userID negative rejected", userID: -1, quantity: 1, expectedErr: domain.ErrInvalidUserID},
		{name: "quantity=0 rejected", userID: 1, quantity: 0, expectedErr: domain.ErrInvalidQuantity},
		{name: "quantity negative rejected", userID: 1, quantity: -3, expectedErr: domain.ErrInvalidQuantity},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockOrderRepo := mocks.NewMockOrderRepository(ctrl)
			mockInventoryRepo := mocks.NewMockInventoryRepository(ctrl)
			// CRITICAL: NO DeductInventory expectation — the alignment
			// fix must reject BEFORE Redis is touched. gomock fails the
			// test if DeductInventory fires unexpectedly.

			service := booking.NewService(mockOrderRepo, mockInventoryRepo)
			order, err := service.BookTicket(ctx, tt.userID, eventID, tt.quantity)

			assert.ErrorIs(t, err, tt.expectedErr,
				"invariant violation must surface the corresponding domain sentinel before Redis is touched")
			assert.Equal(t, domain.Order{}, order)
		})
	}
}
