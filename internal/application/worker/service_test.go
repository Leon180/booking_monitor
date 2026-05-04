package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// SpyingMetrics records outcomes for decorator assertions.
type SpyingMetrics struct {
	Outcomes            []string
	ProcessingDurations []time.Duration
	ConflictCount       int
}

func (s *SpyingMetrics) RecordOrderOutcome(status string) {
	s.Outcomes = append(s.Outcomes, status)
}

func (s *SpyingMetrics) RecordProcessingDuration(d time.Duration) {
	s.ProcessingDurations = append(s.ProcessingDurations, d)
}

func (s *SpyingMetrics) RecordInventoryConflict() {
	s.ConflictCount++
}

// TestOrderMessageProcessor_Process covers the DB-transaction body of
// message processing. The base processor no longer records metrics —
// those assertions moved to TestMessageProcessorMetricsDecorator_Process.
//
// PR 35: the processor now resolves repos through application.Repositories
// inside uow.Do (instead of struct fields). The MockUnitOfWork's Do
// stub builds a Repositories with the individual mock repos and
// invokes fn — same effective coverage, different plumbing.
func TestOrderMessageProcessor_Process(t *testing.T) {
	nopLogger := mlog.NewNop()
	validEventID := uuid.New()
	// D4.1 — KKTIX-aligned ticket type id rides on every QueuedBookingMessage
	// alongside event_id. Tests use a synthetic id since this contract
	// doesn't enforce FK existence.
	validTicketTypeID := uuid.New()
	// validReservedUntil — a future TTL the Pattern A factory will accept.
	// 15 minutes mirrors the production BookingConfig default; tests just
	// need "comfortably in the future" so timing skew across slow CI runs
	// doesn't flip a row from valid to expired before NewReservation runs.
	validReservedUntil := time.Now().Add(15 * time.Minute)

	tests := []struct {
		name          string
		msg           *worker.QueuedBookingMessage
		setupMocks    func(*worker.QueuedBookingMessage, *mocks.MockEventRepository, *mocks.MockOrderRepository, *mocks.MockOutboxRepository, *mocks.MockUnitOfWork)
		expectedError error
	}{
		{
			name: "Success",
			msg:  &worker.QueuedBookingMessage{MessageID: "1-0", OrderID: uuid.New(), EventID: validEventID, TicketTypeID: validTicketTypeID, UserID: 1, Quantity: 1, ReservedUntil: validReservedUntil, AmountCents: 2000, Currency: "usd"},
			setupMocks: func(msg *worker.QueuedBookingMessage, era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(*application.Repositories) error) error {
					return fn(&application.Repositories{Order: ora, Event: era, Outbox: outbox})
				})
				era.EXPECT().DecrementTicket(gomock.Any(), validEventID, 1).Return(nil)
				ora.EXPECT().Create(gomock.Any(), gomock.AssignableToTypeOf(domain.Order{})).DoAndReturn(func(_ context.Context, o domain.Order) (domain.Order, error) {
					// PR-47 contract pin: the worker MUST pass the
					// caller-minted msg.OrderID into NewOrder rather
					// than re-mint internally. Without this assertion,
					// a regression where the worker calls
					// `domain.NewOrder(uuid.New(), ...)` instead of
					// `domain.NewOrder(msg.OrderID, ...)` would still
					// pass the test (any non-zero UUID would).
					assert.Equal(t, msg.OrderID, o.ID(),
						"worker must propagate msg.OrderID end-to-end (not re-mint)")
					return o, nil
				})
				outbox.EXPECT().Create(gomock.Any(), gomock.AssignableToTypeOf(domain.OutboxEvent{})).DoAndReturn(func(_ context.Context, e domain.OutboxEvent) (domain.OutboxEvent, error) {
					assert.Equal(t, domain.EventTypeOrderCreated, e.EventType())
					assert.Equal(t, domain.OutboxStatusPending, e.Status())
					// Verify the order's UUID id + the schema version
					// propagate into the wire payload via the explicit
					// OrderCreatedEvent type — that's the contract the
					// payment consumer expects.
					var eventPayload application.OrderCreatedEvent
					require.NoError(t, json.Unmarshal(e.Payload(), &eventPayload))
					assert.NotEqual(t, uuid.Nil, eventPayload.OrderID, "outbox payload must include the order's UUID")
					assert.Equal(t, validEventID, eventPayload.EventID, "outbox payload event_id must match input")
					assert.Equal(t, application.OrderEventVersion, eventPayload.Version, "outbox payload must carry the schema version")
					return e, nil
				})
			},
			expectedError: nil,
		},
		{
			name: "Inventory Sold Out (DB Conflict)",
			msg:  &worker.QueuedBookingMessage{MessageID: "2-0", OrderID: uuid.New(), EventID: validEventID, TicketTypeID: validTicketTypeID, UserID: 1, Quantity: 1, ReservedUntil: validReservedUntil, AmountCents: 2000, Currency: "usd"},
			setupMocks: func(_ *worker.QueuedBookingMessage, era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(*application.Repositories) error) error {
					return fn(&application.Repositories{Order: ora, Event: era, Outbox: outbox})
				})
				era.EXPECT().DecrementTicket(gomock.Any(), validEventID, 1).Return(domain.ErrSoldOut)
			},
			expectedError: domain.ErrSoldOut,
		},
		{
			name: "Duplicate Purchase (DB Constraint)",
			msg:  &worker.QueuedBookingMessage{MessageID: "3-0", OrderID: uuid.New(), EventID: validEventID, TicketTypeID: validTicketTypeID, UserID: 1, Quantity: 1, ReservedUntil: validReservedUntil, AmountCents: 2000, Currency: "usd"},
			setupMocks: func(_ *worker.QueuedBookingMessage, era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(*application.Repositories) error) error {
					return fn(&application.Repositories{Order: ora, Event: era, Outbox: outbox})
				})
				era.EXPECT().DecrementTicket(gomock.Any(), validEventID, 1).Return(nil)
				ora.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.Order{}, domain.ErrUserAlreadyBought)
			},
			expectedError: domain.ErrUserAlreadyBought,
		},
		{
			name: "DB Error (Create Order)",
			msg:  &worker.QueuedBookingMessage{MessageID: "4-0", OrderID: uuid.New(), EventID: validEventID, TicketTypeID: validTicketTypeID, UserID: 1, Quantity: 1, ReservedUntil: validReservedUntil, AmountCents: 2000, Currency: "usd"},
			setupMocks: func(_ *worker.QueuedBookingMessage, era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, fn func(*application.Repositories) error) error {
					return fn(&application.Repositories{Order: ora, Event: era, Outbox: outbox})
				})
				era.EXPECT().DecrementTicket(gomock.Any(), validEventID, 1).Return(nil)
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
			msg:  &worker.QueuedBookingMessage{MessageID: "5-0", OrderID: uuid.New(), EventID: validEventID, TicketTypeID: validTicketTypeID, UserID: 0, Quantity: 1, ReservedUntil: validReservedUntil, AmountCents: 2000, Currency: "usd"},
			setupMocks: func(_ *worker.QueuedBookingMessage, era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				// Deliberately empty — gomock will fail the test if any
				// repo call fires, asserting fail-fast.
			},
			expectedError: domain.ErrInvalidUserID,
		},
		{
			// PR-47 belt-and-suspenders: parseMessage already rejects
			// zero-UUID order_id at the queue boundary, but the domain
			// factory is the second guard. This case exercises the
			// processor-level path directly (a future refactor that
			// bypasses parseMessage — e.g. a Kafka or NATS adapter —
			// must NOT slip a zero-UUID order through). Same fail-fast
			// expectation as the other malformed cases.
			name: "Malformed message — zero OrderID short-circuits before tx",
			msg:  &worker.QueuedBookingMessage{MessageID: "6-0", OrderID: uuid.Nil, EventID: validEventID, TicketTypeID: validTicketTypeID, UserID: 1, Quantity: 1, ReservedUntil: validReservedUntil, AmountCents: 2000, Currency: "usd"},
			setupMocks: func(_ *worker.QueuedBookingMessage, era *mocks.MockEventRepository, ora *mocks.MockOrderRepository, outbox *mocks.MockOutboxRepository, uow *mocks.MockUnitOfWork) {
				// Deliberately empty — fail-fast assertion.
			},
			expectedError: domain.ErrInvalidOrderID,
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
				tt.setupMocks(tt.msg, mockEventRepo, mockOrderRepo, mockOutbox, mockUoW)
			}

			p := worker.NewOrderMessageProcessor(mockUoW, nopLogger)
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

func (f *fakeMessageProcessor) Process(_ context.Context, _ *worker.QueuedBookingMessage) error {
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
			spy := &SpyingMetrics{}
			decorated := worker.NewMessageProcessorMetricsDecorator(&fakeMessageProcessor{err: tt.innerErr}, spy)

			err := decorated.Process(context.Background(), &worker.QueuedBookingMessage{MessageID: "test"})

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

func TestService_Start(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockQueue := mocks.NewMockOrderQueue(ctrl)
	nopLogger := mlog.NewNop()

	// Processor is opaque to Service — a fake suffices; Start
	// never calls Process directly, it hands the method reference to
	// Subscribe.
	svc := worker.NewService(mockQueue, &fakeMessageProcessor{}, nopLogger)

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
