package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// SpyingWorkerMetrics records outcomes for decorator assertions.
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

// TestOrderMessageProcessor_Process covers the DB-transaction body of
// message processing. The base processor no longer records metrics —
// those assertions moved to TestMessageProcessorMetricsDecorator_Process.
func TestOrderMessageProcessor_Process(t *testing.T) {
	nopLogger := mlog.NewNop()

	tests := []struct {
		name          string
		msg           *domain.OrderMessage
		setupMocks    func(*mocks.MockEventRepository, *mocks.MockOrderRepository, *mocks.MockOutboxRepository, *mocks.MockUnitOfWork)
		expectedError error
	}{
		{
			name: "Success",
			msg:  &domain.OrderMessage{ID: "1-0", EventID: 1, UserID: 1, Quantity: 1},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})
				era.EXPECT().DecrementTicket(gomock.Any(), 1, 1).Return(nil)
				ora.EXPECT().Create(gomock.Any(), gomock.AssignableToTypeOf(domain.Order{})).DoAndReturn(func(_ context.Context, o domain.Order) (domain.Order, error) {
					o.ID = 42 // simulate DB-assigned id; asserted downstream in the outbox payload
					return o, nil
				})
				outbox.EXPECT().Create(gomock.Any(), gomock.AssignableToTypeOf(domain.OutboxEvent{})).DoAndReturn(func(_ context.Context, e domain.OutboxEvent) (domain.OutboxEvent, error) {
					assert.Equal(t, domain.EventTypeOrderCreated, e.EventType)
					assert.Equal(t, domain.OutboxStatusPending, e.Status)
					// Verify the DB-assigned ID + the schema version
					// propagate into the wire payload. Unmarshal into
					// the explicit OrderCreatedEvent type (PR 32) —
					// not domain.Order — because that's the actual
					// contract the payment consumer expects.
					var eventPayload domain.OrderCreatedEvent
					require.NoError(t, json.Unmarshal(e.Payload, &eventPayload))
					assert.Equal(t, 42, eventPayload.OrderID, "outbox payload must include the DB-assigned order ID")
					assert.Equal(t, domain.OrderEventVersion, eventPayload.Version, "outbox payload must carry the schema version")
					e.ID = 99
					return e, nil
				})
			},
			expectedError: nil,
		},
		{
			name: "Inventory Sold Out (DB Conflict)",
			msg:  &domain.OrderMessage{ID: "2-0", EventID: 1, UserID: 1, Quantity: 1},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})
				era.EXPECT().DecrementTicket(gomock.Any(), 1, 1).Return(domain.ErrSoldOut)
			},
			expectedError: domain.ErrSoldOut,
		},
		{
			name: "Duplicate Purchase (DB Constraint)",
			msg:  &domain.OrderMessage{ID: "3-0", EventID: 1, UserID: 1, Quantity: 1},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})
				era.EXPECT().DecrementTicket(gomock.Any(), 1, 1).Return(nil)
				ora.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.Order{}, domain.ErrUserAlreadyBought)
			},
			expectedError: domain.ErrUserAlreadyBought,
		},
		{
			name: "DB Error (Create Order)",
			msg:  &domain.OrderMessage{ID: "4-0", EventID: 1, UserID: 1, Quantity: 1},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(context.Context) error) error {
					return fn(ctx)
				})
				era.EXPECT().DecrementTicket(gomock.Any(), 1, 1).Return(nil)
				ora.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.Order{}, errors.New("db connection failed"))
			},
			expectedError: errors.New("db connection failed"),
		},
		{
			// Validates that NewOrder's invariant violation propagates
			// out of Process unwrapped, so messageProcessorMetricsDecorator
			// can errors.Is-classify it as "malformed_message" instead
			// of "db_error". Also verifies the fail-fast path: the
			// processor MUST NOT open a tx or call DecrementTicket
			// when the message itself is malformed.
			name: "Malformed message — invalid UserID short-circuits before tx",
			msg:  &domain.OrderMessage{ID: "5-0", EventID: 1, UserID: 0, Quantity: 1},
			setupMocks: func(era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				// Deliberately empty: no uow.Do, no DecrementTicket,
				// no Create expectations. gomock will fail the test
				// if any of those fire, which is exactly what we want
				// to assert — a malformed message must not touch the DB.
			},
			expectedError: domain.ErrInvalidUserID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockEventRepo := mocks.NewMockEventRepository(ctrl)
			mockOrderRepo := mocks.NewMockOrderRepository(ctrl)
			mockUoW := mocks.NewMockUnitOfWork(ctrl)
			mockOutbox := mocks.NewMockOutboxRepository(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockEventRepo, mockOrderRepo, mockOutbox, mockUoW)
			}

			p := NewOrderMessageProcessor(mockOrderRepo, mockEventRepo, mockOutbox, mockUoW, nopLogger)
			err := p.Process(context.Background(), tt.msg)

			if tt.expectedError != nil {
				assert.Error(t, err)
				if errors.Is(tt.expectedError, domain.ErrSoldOut) || errors.Is(tt.expectedError, domain.ErrUserAlreadyBought) {
					assert.ErrorIs(t, err, tt.expectedError)
				} else {
					assert.Contains(t, err.Error(), tt.expectedError.Error())
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// fakeMessageProcessor returns a preset error — used to drive the
// metrics decorator without spinning up mocks for the full DB chain.
type fakeMessageProcessor struct {
	err error
}

func (f *fakeMessageProcessor) Process(_ context.Context, _ *domain.OrderMessage) error {
	return f.err
}

// TestMessageProcessorMetricsDecorator_Process verifies the decorator
// maps each error class to the correct metric outcome and always
// records processing duration (even on error paths).
func TestMessageProcessorMetricsDecorator_Process(t *testing.T) {
	tests := []struct {
		name            string
		innerErr        error
		wantOutcome     string
		wantConflictInc bool
	}{
		{name: "Success", innerErr: nil, wantOutcome: "success"},
		{name: "Sold Out", innerErr: domain.ErrSoldOut, wantOutcome: "sold_out", wantConflictInc: true},
		{name: "Duplicate", innerErr: domain.ErrUserAlreadyBought, wantOutcome: "duplicate"},
		{name: "Unknown error falls through to db_error", innerErr: errors.New("something weird"), wantOutcome: "db_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &SpyingWorkerMetrics{}
			decorated := NewMessageProcessorMetricsDecorator(&fakeMessageProcessor{err: tt.innerErr}, spy)

			err := decorated.Process(context.Background(), &domain.OrderMessage{ID: "test"})

			if tt.innerErr == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tt.innerErr)
			}

			assert.Equal(t, []string{tt.wantOutcome}, spy.Outcomes)
			assert.Len(t, spy.ProcessingDurations, 1, "duration must be recorded on every path")
			if tt.wantConflictInc {
				assert.Equal(t, 1, spy.ConflictCount)
			} else {
				assert.Equal(t, 0, spy.ConflictCount)
			}
		})
	}
}

func TestWorkerService_Start(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockQueue := mocks.NewMockOrderQueue(ctrl)
	nopLogger := mlog.NewNop()

	// Processor is opaque to WorkerService — a fake suffices; Start
	// never calls Process directly, it hands the method reference to
	// Subscribe.
	svc := NewWorkerService(mockQueue, &fakeMessageProcessor{}, nopLogger)

	ctx := context.Background()

	// 1. Clean shutdown path: EnsureGroup OK + Subscribe returns nil.
	mockQueue.EXPECT().EnsureGroup(ctx).Return(nil)
	mockQueue.EXPECT().Subscribe(ctx, gomock.Any()).Return(nil)
	assert.NoError(t, svc.Start(ctx))

	// 2. Graceful shutdown: Subscribe returns context.Canceled.
	//    Start must filter it to nil so callers don't escalate SIGINT.
	mockQueue.EXPECT().EnsureGroup(ctx).Return(nil)
	mockQueue.EXPECT().Subscribe(ctx, gomock.Any()).Return(context.Canceled)
	assert.NoError(t, svc.Start(ctx))

	// 3. Real subscribe failure: Start must surface the error wrapped.
	subErr := errors.New("redis connection lost")
	mockQueue.EXPECT().EnsureGroup(ctx).Return(nil)
	mockQueue.EXPECT().Subscribe(ctx, gomock.Any()).Return(subErr)
	err := svc.Start(ctx)
	assert.Error(t, err)
	assert.ErrorIs(t, err, subErr)

	// 4. EnsureGroup failure: must short-circuit (no Subscribe call)
	//    and surface the error.
	ensureErr := errors.New("redis auth failed")
	mockQueue.EXPECT().EnsureGroup(ctx).Return(ensureErr)
	err = svc.Start(ctx)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ensureErr)

	// 5. EnsureGroup returns context.Canceled: treat as clean exit
	//    (shutdown race during startup), no error.
	mockQueue.EXPECT().EnsureGroup(ctx).Return(context.Canceled)
	assert.NoError(t, svc.Start(ctx))
}
