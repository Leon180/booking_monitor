package application_test

import (
	"context"
	"errors"
	"testing"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks" // Generated Mocks

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
		mockSetup     func(*mocks.MockEventRepository, *mocks.MockOrderRepository, *mocks.MockInventoryRepository, *mocks.MockUnitOfWork)
		expectedError error
	}{
		{
			name:     "Success",
			userID:   1,
			eventID:  eventID,
			quantity: 2,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository, u *mocks.MockUnitOfWork) {
				// Phase 6: buyers set removed from Redis, userID still passed for stream publishing
				i.EXPECT().DeductInventory(gomock.Any(), eventID, 1, 2).Return(true, nil)
			},
			expectedError: nil,
		},
		{
			name:     "Sold Out (Redis)",
			userID:   1,
			eventID:  eventID,
			quantity: 5,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository, u *mocks.MockUnitOfWork) {
				// Redis returns false (Sold Out)
				i.EXPECT().DeductInventory(gomock.Any(), eventID, 1, 5).Return(false, nil)
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
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository, u *mocks.MockUnitOfWork) {
				i.EXPECT().DeductInventory(gomock.Any(), eventID, 1, 1).Return(false, errors.New("connection failed"))
			},
			expectedError: errors.New("redis inventory error: connection failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Setup Mocks
			mockEventRepo := mocks.NewMockEventRepository(ctrl)
			mockOrderRepo := mocks.NewMockOrderRepository(ctrl)
			mockInventoryRepo := mocks.NewMockInventoryRepository(ctrl)
			mockUoW := mocks.NewMockUnitOfWork(ctrl)

			if tt.mockSetup != nil {
				tt.mockSetup(mockEventRepo, mockOrderRepo, mockInventoryRepo, mockUoW)
			}

			// Service
			service := application.NewBookingService(mockEventRepo, mockOrderRepo, mockInventoryRepo, mockUoW)

			// Execute
			err := service.BookTicket(ctx, tt.userID, tt.eventID, tt.quantity)

			// Assert
			if tt.expectedError != nil {
				if tt.name == "Redis Error" {
					assert.EqualError(t, err, tt.expectedError.Error())
				} else {
					assert.ErrorIs(t, err, tt.expectedError)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
