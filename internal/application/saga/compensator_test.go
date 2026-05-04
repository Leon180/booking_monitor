package saga_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"booking_monitor/internal/application"
	"booking_monitor/internal/application/saga"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Tests cover compensator.HandleOrderFailed across:
//  - unmarshal failure (malformed JSON)
//  - already-compensated short-circuit (idempotency)
//  - GetByID error → uow returns the wrapped error
//  - IncrementTicket error → uow rollback
//  - MarkCompensated error → uow rollback
//  - successful uow but RevertInventory fails → message stays in PEL
//  - happy path → both DB and Redis sides advance
//
// All tests run with a recording-style UoW that invokes fn synchronously
// against the per-test repository mocks. This mirrors the production
// PostgresUnitOfWork's contract (run fn against per-aggregate repos
// inside a tx) without requiring a real DB.

func compensatorHarness(t *testing.T) (saga.Compensator, *mocks.MockOrderRepository, *mocks.MockEventRepository, *mocks.MockTicketTypeRepository, *mocks.MockInventoryRepository, *mocks.MockUnitOfWork) {
	t.Helper()
	ctrl := gomock.NewController(t)
	orderRepo := mocks.NewMockOrderRepository(ctrl)
	eventRepo := mocks.NewMockEventRepository(ctrl)
	ticketTypeRepo := mocks.NewMockTicketTypeRepository(ctrl)
	invRepo := mocks.NewMockInventoryRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)

	comp := saga.NewCompensator(invRepo, uow, mlog.NewNop())
	return comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow
}

// reconstructOrder is the same shape the watchdog tests use — bypasses
// invariant validation since rehydrated orders represent persisted state.
func reconstructOrder(t *testing.T, id uuid.UUID, eventID uuid.UUID, status domain.OrderStatus) domain.Order {
	t.Helper()
	return domain.ReconstructOrder(id, 1, eventID, uuid.Nil, 1, status, time.Now().Add(-1*time.Hour), time.Time{}, "", 0, "")
}

// runUowThrough is a helper that returns a gomock action which invokes
// the closure fn that production code passes to UoW.Do. Tests then drive
// the inner repos via the harness's mock objects.
//
// LIMITATION — what this simulator does NOT verify:
//
//  1. Commit / rollback semantics. Production PostgresUnitOfWork commits
//     on nil and rolls back on error; this helper just returns whatever
//     fn returns. A regression that mutated state OUTSIDE the closure
//     and relied on rollback to undo it would not be caught here.
//     Rollback correctness is a PostgresUnitOfWork integration concern
//     (covered by the future CP4 testcontainers suite).
//
//  2. Outbox interactions. The compensator's current closure does not
//     touch repos.Outbox, so we explicitly set it to nil. If a future
//     change adds an outbox emit inside the compensator (paralleling
//     the recon force-fail's outbox emit fix in PR #45), that code
//     will nil-panic the moment a test runs — that's intentional, it's
//     a loud signal to update this helper.
func runUowThrough(orderRepo domain.OrderRepository, eventRepo domain.EventRepository, ticketTypeRepo domain.TicketTypeRepository) func(_ context.Context, fn func(*application.Repositories) error) error {
	return func(_ context.Context, fn func(*application.Repositories) error) error {
		return fn(&application.Repositories{Order: orderRepo, Event: eventRepo, TicketType: ticketTypeRepo, Outbox: nil})
	}
}

func newOrderFailedPayload(t *testing.T, orderID, eventID, ticketTypeID uuid.UUID) []byte {
	t.Helper()
	payload, err := json.Marshal(application.OrderFailedEvent{
		OrderID:      orderID,
		EventID:      eventID,
		TicketTypeID: ticketTypeID,
		Quantity:     1,
		Reason:       "test",
	})
	require.NoError(t, err)
	return payload
}

// TestHandleOrderFailed_UnmarshalError: malformed JSON → wrapped error,
// no DB or Redis side effects.
func TestHandleOrderFailed_UnmarshalError(t *testing.T) {
	t.Parallel()
	comp, _, _, _, _, _ := compensatorHarness(t)

	err := comp.HandleOrderFailed(context.Background(), []byte("not json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

// TestHandleOrderFailed_AlreadyCompensated: GetByID returns an order
// already in Compensated state → uow closure returns nil short-circuit.
// IncrementTicket / MarkCompensated MUST NOT be called. Redis revert
// runs anyway because revert.lua is idempotent.
func TestHandleOrderFailed_AlreadyCompensated(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow := compensatorHarness(t)

	orderID, eventID, ticketTypeID := uuid.New(), uuid.New(), uuid.New()
	already := reconstructOrder(t, orderID, eventID, domain.OrderStatusCompensated)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(already, nil)

	// IncrementTicket / MarkCompensated NOT expected — short-circuit.
	invRepo.EXPECT().RevertInventory(gomock.Any(), eventID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID))
	require.NoError(t, err)
}

// TestHandleOrderFailed_GetByIDError: orderRepo.GetByID fails → uow
// closure returns the wrapped error → uow.Do returns it → handler
// returns it without touching Redis. The next saga retry will redo
// from the top.
func TestHandleOrderFailed_GetByIDError(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, _, uow := compensatorHarness(t)

	orderID, eventID, ticketTypeID := uuid.New(), uuid.New(), uuid.New()
	dbErr := errors.New("postgres: read replica unreachable")

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(domain.Order{}, dbErr)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID))
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr)
}

// TestHandleOrderFailed_IncrementTicketError: GetByID succeeds but
// TicketType.IncrementTicket fails → uow rolls back → handler propagates.
// Redis revert MUST NOT run — DB-side compensation incomplete means we
// don't want to half-roll-back.
//
// D4.1 follow-up: IncrementTicket is now on TicketTypeRepository, not
// EventRepository. The semantic contract (failure → rollback → handler
// propagates → no Redis revert) is unchanged.
func TestHandleOrderFailed_IncrementTicketError(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, _, uow := compensatorHarness(t)

	orderID, eventID, ticketTypeID := uuid.New(), uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
	dbErr := errors.New("postgres: lock timeout")

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), ticketTypeID, 1).Return(dbErr)

	// MarkCompensated and RevertInventory NOT expected.
	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID))
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr)
}

// TestHandleOrderFailed_MarkCompensatedError: IncrementTicket succeeds
// but the final MarkCompensated fails → uow rolls back the
// IncrementTicket too (this is precisely why the multi-aggregate UoW
// exists). Handler propagates; Redis revert NOT called.
func TestHandleOrderFailed_MarkCompensatedError(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, _, uow := compensatorHarness(t)

	orderID, eventID, ticketTypeID := uuid.New(), uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
	dbErr := errors.New("postgres: deadlock detected")

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), ticketTypeID, 1).Return(nil)
	orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(dbErr)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID))
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr)
}

// TestHandleOrderFailed_RevertInventoryError: DB side commits cleanly
// but Redis revert fails → handler returns the Redis error. The Kafka
// message stays in PEL → next delivery sees order already in
// Compensated state and idempotently skips the DB side, retrying only
// Redis (revert.lua is itself idempotent).
func TestHandleOrderFailed_RevertInventoryError(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow := compensatorHarness(t)

	orderID, eventID, ticketTypeID := uuid.New(), uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
	redisErr := errors.New("redis: NOSCRIPT")

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), ticketTypeID, 1).Return(nil)
	orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)
	invRepo.EXPECT().RevertInventory(gomock.Any(), eventID, 1, "order:"+orderID.String()).Return(redisErr)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID))
	require.Error(t, err)
	assert.ErrorIs(t, err, redisErr)
}

// TestHandleOrderFailed_HappyPath: both DB and Redis sides advance
// cleanly; handler returns nil; saga consumer XACKs the message.
func TestHandleOrderFailed_HappyPath(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow := compensatorHarness(t)

	orderID, eventID, ticketTypeID := uuid.New(), uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), ticketTypeID, 1).Return(nil)
	orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)
	invRepo.EXPECT().RevertInventory(gomock.Any(), eventID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID))
	require.NoError(t, err)
}

// TestHandleOrderFailed_LegacyEvent_FallsBackToSingleTicketType:
// Path B of the three-path resolution. A pre-v3 event in flight has
// TicketTypeID == uuid.Nil. The compensator falls back to
// ListByEventID; D4.1 default-single-ticket-type-per-event means
// exactly 1 row → use that id and proceed normally.
func TestHandleOrderFailed_LegacyEvent_FallsBackToSingleTicketType(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow := compensatorHarness(t)

	orderID, eventID, fallbackTicketTypeID := uuid.New(), uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
	// Reconstruct the single ticket_type row that ListByEventID returns.
	tt := domain.ReconstructTicketType(fallbackTicketTypeID, eventID, "Default", 2000, "usd", 100, 100, nil, nil, nil, "", 0)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	// Legacy fallback: ListByEventID called; returns exactly 1 row.
	ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return([]domain.TicketType{tt}, nil)
	// IncrementTicket called against the fallback id, NOT the (zero) one in the event.
	ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), fallbackTicketTypeID, 1).Return(nil)
	orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)
	invRepo.EXPECT().RevertInventory(gomock.Any(), eventID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, uuid.Nil))
	require.NoError(t, err)
}

// TestHandleOrderFailed_LegacyEvent_NoTicketTypes_SkipsIncrement:
// Path C of the three-path resolution. ListByEventID returns 0 rows
// → DB increment skipped (manual review required), but
// MarkCompensated + Redis revert still proceed because the
// user-visible inventory (Redis) remains authoritative.
func TestHandleOrderFailed_LegacyEvent_NoTicketTypes_SkipsIncrement(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow := compensatorHarness(t)

	orderID, eventID := uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	// Legacy fallback finds zero ticket_types — corruption case.
	ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return(nil, nil)
	// IncrementTicket NOT expected (Path C skip).
	orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)
	invRepo.EXPECT().RevertInventory(gomock.Any(), eventID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, uuid.Nil))
	require.NoError(t, err)
}

// TestHandleOrderFailed_LegacyEvent_MultipleTicketTypes_SkipsIncrement:
// Path C of the three-path resolution, multi-row variant. > 1
// ticket_type per event + missing TicketTypeID is unrecoverable —
// picking the wrong one would corrupt the visible counter, so the
// compensator skips DB increment and relies on Redis (which is keyed
// by event_id and unambiguous).
func TestHandleOrderFailed_LegacyEvent_MultipleTicketTypes_SkipsIncrement(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow := compensatorHarness(t)

	orderID, eventID := uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
	tt1 := domain.ReconstructTicketType(uuid.New(), eventID, "VIP", 5000, "usd", 100, 100, nil, nil, nil, "", 0)
	tt2 := domain.ReconstructTicketType(uuid.New(), eventID, "General", 2000, "usd", 100, 100, nil, nil, nil, "", 0)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return([]domain.TicketType{tt1, tt2}, nil)
	// IncrementTicket NOT expected (Path C skip).
	orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)
	invRepo.EXPECT().RevertInventory(gomock.Any(), eventID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, uuid.Nil))
	require.NoError(t, err)
}
