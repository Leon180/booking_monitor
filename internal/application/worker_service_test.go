package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/mocks"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"
)

// SpyingWorkerMetrics for verifying metric recording
type SpyingWorkerMetrics struct {
	Outcomes            []string
	ProcessingDurations []time.Duration
	ConflictCount       int
}

func (s *SpyingWorkerMetrics) RecordOrderOutcome(status string) {
	s.Outcomes = append(s.Outcomes, status)
}

func (s *SpyingWorkerMetrics) RecordProcessingDuration(d time.Duration) {
	s.ProcessingDurations = append(s.ProcessingDurations, d)
}

func (s *SpyingWorkerMetrics) RecordInventoryConflict() {
	s.ConflictCount++
}

func TestWorkerService_ProcessMessage(t *testing.T) {
	// Initialize logger
	nopLogger := zap.NewNop().Sugar()

	tests := []struct {
		name           string
		msg            *domain.OrderMessage
		setupMocks     func(*mocks.MockEventRepository, *mocks.MockOrderRepository, *mocks.MockOutboxRepository, *mocks.MockOrderQueue, *mocks.MockUnitOfWork)
		expectedError  error
		expectedMetric string
	}{
		{
			name: "Success",
			msg: &domain.OrderMessage{
				ID: "1-0", EventID: 1, UserID: 1, Quantity: 1,
			},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, queue *mocks.MockOrderQueue, uow *mocks.MockUnitOfWork) {
				// 1. Transaction wrapper
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})

				// 2. Decrement Ticket (Source of Truth)
				era.EXPECT().DecrementTicket(gomock.Any(), 1, 1).Return(nil)

				// 3. Create Order
				ora.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

				// 4. Create Outbox Event
				outbox.EXPECT().Create(gomock.Any(), gomock.AssignableToTypeOf(&domain.OutboxEvent{})).DoAndReturn(func(_ context.Context, e *domain.OutboxEvent) error {
					assert.Equal(t, "order_created", e.EventType)
					assert.Equal(t, "PENDING", e.Status)
					return nil
				})
			},
			expectedError:  nil,
			expectedMetric: "success",
		},
		{
			name: "Inventory Sold Out (DB Conflict)",
			msg: &domain.OrderMessage{
				ID: "2-0", EventID: 1, UserID: 1, Quantity: 1,
			},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, queue *mocks.MockOrderQueue, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})

				// DB says Sold Out
				era.EXPECT().DecrementTicket(gomock.Any(), 1, 1).Return(domain.ErrSoldOut)
			},
			expectedError:  domain.ErrSoldOut,
			expectedMetric: "sold_out",
		},
		{
			name: "Duplicate Purchase (DB Constraint)",
			msg: &domain.OrderMessage{
				ID: "3-0", EventID: 1, UserID: 1, Quantity: 1,
			},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, queue *mocks.MockOrderQueue, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})

				era.EXPECT().DecrementTicket(gomock.Any(), 1, 1).Return(nil)

				// DB says Duplicate (ErrUserAlreadyBought)
				ora.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.ErrUserAlreadyBought)
			},
			expectedError:  domain.ErrUserAlreadyBought,
			expectedMetric: "duplicate",
		},
		{
			name: "DB Error (Create Order)",
			msg: &domain.OrderMessage{
				ID: "4-0", EventID: 1, UserID: 1, Quantity: 1,
			},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, queue *mocks.MockOrderQueue, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})

				era.EXPECT().DecrementTicket(gomock.Any(), 1, 1).Return(nil)

				// Random DB error
				ora.EXPECT().Create(gomock.Any(), gomock.Any()).Return(errors.New("db connection failed"))
			},
			expectedError:  errors.New("db connection failed"),
			expectedMetric: "db_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Mocks
			mockEventRepo := mocks.NewMockEventRepository(ctrl)
			mockOrderRepo := mocks.NewMockOrderRepository(ctrl)
			mockUoW := mocks.NewMockUnitOfWork(ctrl)
			mockOutbox := mocks.NewMockOutboxRepository(ctrl)
			mockQueue := mocks.NewMockOrderQueue(ctrl)

			// Metrics Spy
			spyMetrics := &SpyingWorkerMetrics{}

			// Setup
			if tt.setupMocks != nil {
				tt.setupMocks(mockEventRepo, mockOrderRepo, mockOutbox, mockQueue, mockUoW)
			}

			svc := &workerService{
				queue:      mockQueue,
				orderRepo:  mockOrderRepo,
				eventRepo:  mockEventRepo,
				outboxRepo: mockOutbox,
				uow:        mockUoW,
				metrics:    spyMetrics,
				logger:     nopLogger,
			}

			// Execute
			err := svc.processMessage(context.Background(), tt.msg)

			// Assert Error
			if tt.expectedError != nil {
				assert.Error(t, err)
				if tt.expectedError == domain.ErrSoldOut || tt.expectedError == domain.ErrUserAlreadyBought {
					assert.ErrorIs(t, err, tt.expectedError)
				} else {
					assert.Contains(t, err.Error(), tt.expectedError.Error())
				}
			} else {
				assert.NoError(t, err)
			}

			// Assert Metrics
			if tt.expectedMetric != "" {
				assert.NotEmpty(t, spyMetrics.Outcomes, "Metrics should be recorded")
				assert.Contains(t, spyMetrics.Outcomes, tt.expectedMetric, "Expected metric outcome not found")
			}
		})
	}
}
