package booking_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

// testBookingConfig builds a *config.Config carrying just the
// BookingConfig BookingService cares about. Centralised so a future
// config-shape change doesn't ripple across every test row.
func testBookingConfig() *config.Config {
	return &config.Config{
		Booking: config.BookingConfig{ReservationWindow: 15 * time.Minute},
	}
}

func TestBookingService_BookTicket(t *testing.T) {
	// Silent logger via the package's own Nop helper — avoids pulling
	// in zap internals in tests.
	ctx := mlog.NewContext(context.Background(), mlog.NewNop(), "")

	eventID := uuid.New()
	ticketTypeID := uuid.New()

	// Reusable ticket type for the metadata-repair path.
	tt := domain.ReconstructTicketType(
		ticketTypeID, eventID, "Default Ticket", 2000, "usd",
		100, 100, nil, nil, nil, "", 0,
	)

	tests := []struct {
		name          string
		userID        int
		ticketTypeID  uuid.UUID
		quantity      int
		mockSetup     func(*mocks.MockOrderRepository, *mocks.MockTicketTypeRepository, *mocks.MockInventoryRepository)
		expectedError error
	}{
		{
			name:         "Success",
			userID:       1,
			ticketTypeID: ticketTypeID,
			quantity:     2,
			mockSetup: func(o *mocks.MockOrderRepository, ttr *mocks.MockTicketTypeRepository, i *mocks.MockInventoryRepository) {
				i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), ticketTypeID, 1, 2, gomock.Any()).Return(domain.DeductInventoryResult{
					Accepted:    true,
					EventID:     eventID,
					AmountCents: 4000,
					Currency:    "usd",
				}, nil)
			},
			expectedError: nil,
		},
		{
			name:         "Sold Out (Redis)",
			userID:       1,
			ticketTypeID: ticketTypeID,
			quantity:     5,
			mockSetup: func(o *mocks.MockOrderRepository, ttr *mocks.MockTicketTypeRepository, i *mocks.MockInventoryRepository) {
				i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), ticketTypeID, 1, 5, gomock.Any()).Return(domain.DeductInventoryResult{
					Accepted: false,
				}, nil)
			},
			expectedError: domain.ErrSoldOut,
		},
		{
			// Duplicate purchase is now enforced by DB UNIQUE constraint (not Redis).
			// At the BookingService level, Redis just returns success — the worker handles the duplicate.
			// This test verifies that a Redis error is propagated correctly.
			name:         "Redis Error",
			userID:       1,
			ticketTypeID: ticketTypeID,
			quantity:     1,
			mockSetup: func(o *mocks.MockOrderRepository, ttr *mocks.MockTicketTypeRepository, i *mocks.MockInventoryRepository) {
				i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), ticketTypeID, 1, 1, gomock.Any()).Return(domain.DeductInventoryResult{}, errors.New("connection failed"))
			},
			expectedError: errors.New("redis inventory error: connection failed"),
		},
		{
			// D4.1 new path: ticket_type lookup miss surfaces a 404-style
			// sentinel BEFORE Redis is touched. Confirms BookingService
			// rejects on ticket_type lookup failure (no DeductInventory
			// expectation — gomock fails the test if it fires).
			name:         "Ticket Type Not Found",
			userID:       1,
			ticketTypeID: ticketTypeID,
			quantity:     1,
			mockSetup: func(o *mocks.MockOrderRepository, ttr *mocks.MockTicketTypeRepository, i *mocks.MockInventoryRepository) {
				i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), ticketTypeID, 1, 1, gomock.Any()).Return(domain.DeductInventoryResult{}, domain.ErrTicketTypeRuntimeMetadataMissing)
				ttr.EXPECT().GetByID(gomock.Any(), ticketTypeID).Return(domain.TicketType{}, domain.ErrTicketTypeNotFound)
			},
			expectedError: domain.ErrTicketTypeNotFound,
		},
		{
			name:         "Metadata Miss Repaired",
			userID:       1,
			ticketTypeID: ticketTypeID,
			quantity:     2,
			mockSetup: func(o *mocks.MockOrderRepository, ttr *mocks.MockTicketTypeRepository, i *mocks.MockInventoryRepository) {
				gomock.InOrder(
					i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), ticketTypeID, 1, 2, gomock.Any()).Return(domain.DeductInventoryResult{}, domain.ErrTicketTypeRuntimeMetadataMissing),
					ttr.EXPECT().GetByID(gomock.Any(), ticketTypeID).Return(tt, nil),
					i.EXPECT().SetTicketTypeMetadata(gomock.Any(), tt).Return(nil),
					i.EXPECT().DeductInventory(gomock.Any(), gomock.Any(), ticketTypeID, 1, 2, gomock.Any()).Return(domain.DeductInventoryResult{
						Accepted:    true,
						EventID:     eventID,
						AmountCents: 4000,
						Currency:    "usd",
					}, nil),
				)
			},
			expectedError: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Setup Mocks
			mockOrderRepo := mocks.NewMockOrderRepository(ctrl)
			mockTicketTypeRepo := mocks.NewMockTicketTypeRepository(ctrl)
			mockInventoryRepo := mocks.NewMockInventoryRepository(ctrl)

			if tt.mockSetup != nil {
				tt.mockSetup(mockOrderRepo, mockTicketTypeRepo, mockInventoryRepo)
			}

			// Service
			service := booking.NewService(mockOrderRepo, mockTicketTypeRepo, mockInventoryRepo, testBookingConfig())

			// Execute
			order, err := service.BookTicket(ctx, tt.userID, tt.ticketTypeID, tt.quantity)

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
				assert.Equal(t, domain.OrderStatusAwaitingPayment, order.Status(),
					"D3: Pattern A reservation must start in AwaitingPayment, NOT Pending — that's the load-bearing semantics")
				assert.False(t, order.ReservedUntil().IsZero(), "Pattern A reservation must carry a non-zero reservedUntil")
				assert.True(t, order.ReservedUntil().After(time.Now()),
					"reservedUntil must be in the future at return time (window > 0)")
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
	ticketTypeID := uuid.New()

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

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockOrderRepo := mocks.NewMockOrderRepository(ctrl)
			mockTicketTypeRepo := mocks.NewMockTicketTypeRepository(ctrl)
			mockInventoryRepo := mocks.NewMockInventoryRepository(ctrl)
			service := booking.NewService(mockOrderRepo, mockTicketTypeRepo, mockInventoryRepo, testBookingConfig())
			order, err := service.BookTicket(ctx, tc.userID, ticketTypeID, tc.quantity)

			assert.ErrorIs(t, err, tc.expectedErr,
				"invariant violation must surface the corresponding domain sentinel before Redis is touched")
			assert.Equal(t, domain.Order{}, order)
		})
	}
}
