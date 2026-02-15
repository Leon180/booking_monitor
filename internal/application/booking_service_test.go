package application_test

import (
	"context"
	"errors"
	"testing"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/mocks" // Generated Mocks
	"booking_monitor/pkg/logger"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

func TestBookingService_BookTicket(t *testing.T) {
	// Initialize a no-op logger for testing
	nopLogger := zap.NewNop().Sugar()
	ctx := logger.WithCtx(context.Background(), nopLogger)

	tests := []struct {
		name          string
		userID        int
		eventID       int
		quantity      int
		mockSetup     func(*mocks.MockEventRepository, *mocks.MockOrderRepository, *mocks.MockInventoryRepository, *mocks.MockUnitOfWork)
		expectedError error
	}{
		{
			name:     "Success",
			userID:   1,
			eventID:  1,
			quantity: 2,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository, u *mocks.MockUnitOfWork) {
				// Phase 2: Expect Redis Deduct
				i.EXPECT().DeductInventory(gomock.Any(), 1, 2).Return(true, nil)
				// Order creation skipped in Phase 2
			},
			expectedError: nil,
		},
		{
			name:     "Sold Out (Redis)",
			userID:   1,
			eventID:  1,
			quantity: 5,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository, u *mocks.MockUnitOfWork) {
				// Redis returns false (Sold Out)
				i.EXPECT().DeductInventory(gomock.Any(), 1, 5).Return(false, nil)
			},
			expectedError: domain.ErrSoldOut,
		},
		{
			name:     "Redis Error",
			userID:   1,
			eventID:  1,
			quantity: 1,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, i *mocks.MockInventoryRepository, u *mocks.MockUnitOfWork) {
				i.EXPECT().DeductInventory(gomock.Any(), 1, 1).Return(false, errors.New("connection failed"))
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
				if tt.name == "Invalid Quantity" {
					assert.EqualError(t, err, tt.expectedError.Error())
				} else {
					assert.Error(t, err)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
