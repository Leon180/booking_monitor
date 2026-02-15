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
		mockSetup     func(*mocks.MockEventRepository, *mocks.MockOrderRepository, *mocks.MockUnitOfWork)
		expectedError error
	}{
		{
			name:     "Success",
			userID:   1,
			eventID:  1,
			quantity: 2,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, u *mocks.MockUnitOfWork) {
				event := &domain.Event{ID: 1, AvailableTickets: 10}

				// UoW Do passthrough
				u.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})

				e.EXPECT().GetByID(gomock.Any(), 1).Return(event, nil)
				e.EXPECT().Update(gomock.Any(), event).Return(nil)

				// Match Order fields
				o.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, order *domain.Order) error {
					if order.UserID != 1 || order.EventID != 1 || order.Quantity != 2 {
						return errors.New("order fields mismatch")
					}
					return nil
				})
			},
			expectedError: nil,
		},
		{
			name:     "Sold Out Domain Logic",
			userID:   1,
			eventID:  1,
			quantity: 5,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, u *mocks.MockUnitOfWork) {
				event := &domain.Event{ID: 1, AvailableTickets: 2} // Less than requested

				u.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})

				e.EXPECT().GetByID(gomock.Any(), 1).Return(event, nil)
				// Update and Create should NOT be called
			},
			expectedError: domain.ErrSoldOut,
		},
		{
			name:     "Event Not Found",
			userID:   1,
			eventID:  99,
			quantity: 1,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, u *mocks.MockUnitOfWork) {
				u.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})

				e.EXPECT().GetByID(gomock.Any(), 99).Return(nil, domain.ErrEventNotFound)
			},
			expectedError: domain.ErrEventNotFound,
		},
		{
			name:     "Invalid Quantity",
			userID:   1,
			eventID:  1,
			quantity: 0,
			mockSetup: func(e *mocks.MockEventRepository, o *mocks.MockOrderRepository, u *mocks.MockUnitOfWork) {
				// No mocks needed as validation happens before UoW
			},
			expectedError: errors.New("invalid quantity: must be between 1 and 10"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Setup Mocks
			mockEventRepo := mocks.NewMockEventRepository(ctrl)
			mockOrderRepo := mocks.NewMockOrderRepository(ctrl)
			mockUoW := mocks.NewMockUnitOfWork(ctrl)

			if tt.mockSetup != nil {
				tt.mockSetup(mockEventRepo, mockOrderRepo, mockUoW)
			}

			// Service
			service := application.NewBookingService(mockEventRepo, mockOrderRepo, mockUoW)

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
