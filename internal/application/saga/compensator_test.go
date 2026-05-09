package saga_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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

	comp := saga.NewCompensator(invRepo, uow, mlog.NewNop(), saga.NopCompensatorMetrics{})
	return comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow
}

// compensatorHarnessWithSpy wires a recording metrics spy instead
// of NopCompensatorMetrics so tests can assert the exact
// RecordEventProcessed / ObserveLoopDuration / SetConsumerLag
// calls. Used by TestHandleOrderFailed_OutcomeLabel_Exhaustive
// (PR-D12.4 Slice 4) to pin the per-branch outcome classification.
func compensatorHarnessWithSpy(t *testing.T) (saga.Compensator, *mocks.MockOrderRepository, *mocks.MockEventRepository, *mocks.MockTicketTypeRepository, *mocks.MockInventoryRepository, *mocks.MockUnitOfWork, *recordingCompensatorMetrics) {
	t.Helper()
	ctrl := gomock.NewController(t)
	orderRepo := mocks.NewMockOrderRepository(ctrl)
	eventRepo := mocks.NewMockEventRepository(ctrl)
	ticketTypeRepo := mocks.NewMockTicketTypeRepository(ctrl)
	invRepo := mocks.NewMockInventoryRepository(ctrl)
	uow := mocks.NewMockUnitOfWork(ctrl)

	spy := &recordingCompensatorMetrics{}
	comp := saga.NewCompensator(invRepo, uow, mlog.NewNop(), spy)
	return comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow, spy
}

// recordingCompensatorMetrics captures every metric call so tests
// can assert the exact outcomes / loop durations / lag values
// emitted by `Compensator.HandleOrderFailed`. Concurrent-safe
// because (a) HandleOrderFailed is called serially per saga
// consumer message in production, and (b) tests may use this with
// t.Parallel() — the mutex guarantees test-shared instances behave
// correctly even though the production caller is single-threaded.
type recordingCompensatorMetrics struct {
	mu                  sync.Mutex
	outcomes            []string
	loopDurations       []time.Duration
	consumerLagSettings []time.Duration
}

func (r *recordingCompensatorMetrics) RecordEventProcessed(outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
}

func (r *recordingCompensatorMetrics) ObserveLoopDuration(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loopDurations = append(r.loopDurations, d)
}

func (r *recordingCompensatorMetrics) SetConsumerLag(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consumerLagSettings = append(r.consumerLagSettings, d)
}

func (r *recordingCompensatorMetrics) Outcomes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.outcomes))
	copy(out, r.outcomes)
	return out
}

func (r *recordingCompensatorMetrics) LoopDurationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.loopDurations)
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

	err := comp.HandleOrderFailed(context.Background(), []byte("not json"), time.Now())
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
	invRepo.EXPECT().RevertInventory(gomock.Any(), ticketTypeID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID), time.Now())
	require.NoError(t, err)
}

// TestHandleOrderFailed_AlreadyCompensated_LegacyEventStillRevertsRedis:
// a prior delivery may have already marked the DB order compensated but
// failed during Redis revert. On retry, a legacy payload still needs the
// fallback ticket_type resolution so the idempotent Redis revert can run.
func TestHandleOrderFailed_AlreadyCompensated_LegacyEventStillRevertsRedis(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow := compensatorHarness(t)

	orderID, eventID, fallbackTicketTypeID := uuid.New(), uuid.New(), uuid.New()
	already := reconstructOrder(t, orderID, eventID, domain.OrderStatusCompensated)
	tt := domain.ReconstructTicketType(fallbackTicketTypeID, eventID, "Default", 2000, "usd", 100, 100, nil, nil, nil, "", 0)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(already, nil)
	ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return([]domain.TicketType{tt}, nil)
	invRepo.EXPECT().RevertInventory(gomock.Any(), fallbackTicketTypeID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, uuid.Nil), time.Now())
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

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID), time.Now())
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
	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID), time.Now())
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

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID), time.Now())
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
	invRepo.EXPECT().RevertInventory(gomock.Any(), ticketTypeID, 1, "order:"+orderID.String()).Return(redisErr)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID), time.Now())
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
	invRepo.EXPECT().RevertInventory(gomock.Any(), ticketTypeID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, ticketTypeID), time.Now())
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
	invRepo.EXPECT().RevertInventory(gomock.Any(), fallbackTicketTypeID, 1, "order:"+orderID.String()).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, uuid.Nil), time.Now())
	require.NoError(t, err)
}

// TestHandleOrderFailed_LegacyEvent_NoTicketTypes_SkipsIncrement:
// Path C of the three-path resolution. ListByEventID returns 0 rows
	// → DB increment skipped and Redis revert is also skipped because
	// the compensator cannot safely identify the runtime key.
func TestHandleOrderFailed_LegacyEvent_NoTicketTypes_SkipsIncrement(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, _, uow := compensatorHarness(t)

	orderID, eventID := uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	// Legacy fallback finds zero ticket_types — corruption case.
	ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return(nil, nil)
	// IncrementTicket and RevertInventory NOT expected (Path C skip).
	orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, uuid.Nil), time.Now())
	require.NoError(t, err)
}

// TestHandleOrderFailed_LegacyEvent_MultipleTicketTypes_SkipsIncrement:
// Path C of the three-path resolution, multi-row variant. > 1
// ticket_type per event + missing TicketTypeID is unrecoverable —
// picking the wrong one would corrupt the visible counter, so the
	// compensator skips DB increment and Redis revert because there is
	// no safe ticket_type key to target.
func TestHandleOrderFailed_LegacyEvent_MultipleTicketTypes_SkipsIncrement(t *testing.T) {
	t.Parallel()
	comp, orderRepo, eventRepo, ticketTypeRepo, _, uow := compensatorHarness(t)

	orderID, eventID := uuid.New(), uuid.New()
	failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
	tt1 := domain.ReconstructTicketType(uuid.New(), eventID, "VIP", 5000, "usd", 100, 100, nil, nil, nil, "", 0)
	tt2 := domain.ReconstructTicketType(uuid.New(), eventID, "General", 2000, "usd", 100, 100, nil, nil, nil, "", 0)

	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
	ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return([]domain.TicketType{tt1, tt2}, nil)
	// IncrementTicket and RevertInventory NOT expected (Path C skip).
	orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)

	err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, uuid.Nil), time.Now())
	require.NoError(t, err)
}

// TestHandleOrderFailed_OutcomeLabel_Exhaustive (PR-D12.4 Slice 4)
// pins one test case per documented outcome label that
// `Compensator.HandleOrderFailed` can emit. Each subtest sets up
// the mock chain to drive the exact branch that produces the
// outcome, then asserts the recording spy received that outcome
// (and ONLY that outcome — `unknown` should never fire on a path
// covered by an explicit `record()` call).
//
// Histogram observation discipline: `ObserveLoopDuration` MUST be
// called exactly once on the `compensated` path AND zero times
// on every other path (including `already_compensated` and
// `path_c_skipped` no-op success paths — sub-millisecond no-ops
// would skew p50/p99 to the floor and hide real degradation).
//
// This is the regression tripwire for the `did_record` sentinel +
// per-branch classifier introduced in Slice 2: a future refactor
// that adds a return path without `record()` lands here as a
// failing assertion (the deferred sentinel records "unknown",
// which the wantOutcomes list does not contain). Codex round-2
// review of PR #106 + multi-agent review of Slice 1+2 caught the
// equivalent gap on the EventPublisher / outcome paths in those
// slices; this test prevents the same shape from re-emerging.
func TestHandleOrderFailed_OutcomeLabel_Exhaustive(t *testing.T) {
	cases := []struct {
		name              string
		setupMocks        func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (orderID, eventID, ticketTypeID uuid.UUID)
		legacyEvent       bool // if true, use uuid.Nil for ticket_type_id in the payload (Path B/C trigger)
		wantOutcome       string
		wantHistogramHit  bool
		wantNoError       bool // success paths return nil; error paths return an error
	}{
		{
			name:             "compensated_full_path_observes_histogram",
			wantOutcome:      "compensated",
			wantHistogramHit: true,
			wantNoError:      true,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
				ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), ttID, 1).Return(nil)
				orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)
				invRepo.EXPECT().RevertInventory(gomock.Any(), ttID, 1, "order:"+orderID.String()).Return(nil)
				return orderID, eventID, ttID
			},
		},
		{
			name:             "already_compensated_skips_histogram",
			wantOutcome:      "already_compensated",
			wantHistogramHit: false,
			wantNoError:      true,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				already := reconstructOrder(t, orderID, eventID, domain.OrderStatusCompensated)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(already, nil)
				invRepo.EXPECT().RevertInventory(gomock.Any(), ttID, 1, "order:"+orderID.String()).Return(nil)
				return orderID, eventID, ttID
			},
		},
		{
			// Path C × already-compensated cross-product. Order arrives
			// already in `compensated` state (Kafka at-least-once
			// redelivery) AND the legacy payload's uuid.Nil triggers
			// resolveTicketTypeID's case-0 branch (no ticket_types for
			// the event — pre-D4.1 data corruption window). End-state:
			// `wasAlreadyCompensated = true` AND
			// `ticketTypeIDForRevert == uuid.Nil`.
			//
			// At compensator.go:257-272 this lands the `already_compensated`
			// outcome (NOT `path_c_skipped` — the `wasAlreadyCompensated`
			// disambiguator is what distinguishes the two). No
			// RevertInventory call — the uuid.Nil early-return skips it.
			//
			// Closes Slice 4 review M2 finding: the existing
			// `already_compensated_skips_histogram` covers Path A (ttID
			// non-nil); this case covers the Path C cross-product.
			name:             "already_compensated_via_path_c_skips_histogram",
			wantOutcome:      "already_compensated",
			wantHistogramHit: false,
			wantNoError:      true,
			legacyEvent:      true,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID := uuid.New(), uuid.New()
				already := reconstructOrder(t, orderID, eventID, domain.OrderStatusCompensated)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(already, nil)
				// 0 ticket_types → resolveTicketTypeID returns (uuid.Nil, nil).
				ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return([]domain.TicketType{}, nil)
				// No RevertInventory expectation — uuid.Nil early-return at 257 skips it.
				// No MarkCompensated expectation — already-compensated branch returns at 177.
				return orderID, eventID, uuid.Nil
			},
		},
		{
			name:             "already_compensated_redis_error_skips_histogram",
			wantOutcome:      "already_compensated_redis_error",
			wantHistogramHit: false,
			wantNoError:      false, // RevertInventory returns an error → method returns wrapped error
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				already := reconstructOrder(t, orderID, eventID, domain.OrderStatusCompensated)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(already, nil)
				invRepo.EXPECT().RevertInventory(gomock.Any(), ttID, 1, "order:"+orderID.String()).Return(errors.New("redis: connection refused"))
				return orderID, eventID, ttID
			},
		},
		{
			name:             "path_c_skipped_skips_histogram",
			wantOutcome:      "path_c_skipped",
			wantHistogramHit: false,
			wantNoError:      true,
			legacyEvent:      true,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID := uuid.New(), uuid.New()
				failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
				tt1 := domain.ReconstructTicketType(uuid.New(), eventID, "VIP", 5000, "usd", 100, 100, nil, nil, nil, "", 0)
				tt2 := domain.ReconstructTicketType(uuid.New(), eventID, "GA", 2000, "usd", 100, 100, nil, nil, nil, "", 0)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
				ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return([]domain.TicketType{tt1, tt2}, nil)
				orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)
				return orderID, eventID, uuid.Nil
			},
		},
		{
			name:             "getbyid_error_skips_histogram",
			wantOutcome:      "getbyid_error",
			wantHistogramHit: false,
			wantNoError:      false,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(domain.Order{}, errors.New("postgres: connection lost"))
				return orderID, eventID, ttID
			},
		},
		{
			name:             "list_ticket_type_error_skips_histogram",
			wantOutcome:      "list_ticket_type_error",
			wantHistogramHit: false,
			wantNoError:      false,
			legacyEvent:      true,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID := uuid.New(), uuid.New()
				failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
				ticketTypeRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return(nil, errors.New("postgres: read replica unreachable"))
				return orderID, eventID, uuid.Nil
			},
		},
		{
			name:             "incrementticket_error_skips_histogram",
			wantOutcome:      "incrementticket_error",
			wantHistogramHit: false,
			wantNoError:      false,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
				ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), ttID, 1).Return(errors.New("postgres: deadlock"))
				return orderID, eventID, ttID
			},
		},
		{
			name:             "markcompensated_error_skips_histogram",
			wantOutcome:      "markcompensated_error",
			wantHistogramHit: false,
			wantNoError:      false,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
				ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), ttID, 1).Return(nil)
				orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(errors.New("postgres: invalid state transition"))
				return orderID, eventID, ttID
			},
		},
		{
			name:             "redis_revert_error_skips_histogram",
			wantOutcome:      "redis_revert_error",
			wantHistogramHit: false,
			wantNoError:      false,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				failed := reconstructOrder(t, orderID, eventID, domain.OrderStatusFailed)
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(runUowThrough(orderRepo, eventRepo, ticketTypeRepo))
				orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(failed, nil)
				ticketTypeRepo.EXPECT().IncrementTicket(gomock.Any(), ttID, 1).Return(nil)
				orderRepo.EXPECT().MarkCompensated(gomock.Any(), orderID).Return(nil)
				invRepo.EXPECT().RevertInventory(gomock.Any(), ttID, 1, "order:"+orderID.String()).Return(errors.New("redis: connection refused"))
				return orderID, eventID, ttID
			},
		},
		{
			name:             "context_error_skips_histogram",
			wantOutcome:      "context_error",
			wantHistogramHit: false,
			wantNoError:      false,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				// UoW returns context.Canceled directly (not wrapped in stepError).
				// The classifier should catch this via errors.Is(errUow, context.Canceled).
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).Return(context.Canceled)
				return orderID, eventID, ttID
			},
		},
		{
			name:             "uow_infra_error_skips_histogram",
			wantOutcome:      "uow_infra_error",
			wantHistogramHit: false,
			wantNoError:      false,
			setupMocks: func(t *testing.T, orderRepo *mocks.MockOrderRepository, eventRepo *mocks.MockEventRepository, ticketTypeRepo *mocks.MockTicketTypeRepository, invRepo *mocks.MockInventoryRepository, uow *mocks.MockUnitOfWork) (uuid.UUID, uuid.UUID, uuid.UUID) {
				orderID, eventID, ttID := uuid.New(), uuid.New(), uuid.New()
				// UoW returns an unwrapped infra error (NOT a stepError, NOT context.Canceled).
				// Simulates a tx-begin failure from the UoW machinery itself.
				uow.EXPECT().Do(gomock.Any(), gomock.Any()).Return(errors.New("postgres: failed to begin transaction"))
				return orderID, eventID, ttID
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			comp, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow, spy := compensatorHarnessWithSpy(t)
			orderID, eventID, ttID := tc.setupMocks(t, orderRepo, eventRepo, ticketTypeRepo, invRepo, uow)

			payloadTicketTypeID := ttID
			if tc.legacyEvent {
				payloadTicketTypeID = uuid.Nil
			}
			err := comp.HandleOrderFailed(context.Background(), newOrderFailedPayload(t, orderID, eventID, payloadTicketTypeID), time.Now())

			if tc.wantNoError {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}

			outcomes := spy.Outcomes()
			require.Len(t, outcomes, 1, "exactly one outcome MUST be recorded; got: %v", outcomes)
			assert.Equal(t, tc.wantOutcome, outcomes[0],
				"branch must record %q, NOT %q (regression in did_record sentinel or per-branch classifier)",
				tc.wantOutcome, outcomes[0])
			assert.NotEqual(t, "unknown", outcomes[0],
				"normal paths must NEVER trip the deferred-sentinel \"unknown\" — that label is reserved for code regressions")

			if tc.wantHistogramHit {
				assert.Equal(t, 1, spy.LoopDurationCount(),
					"compensated success path MUST observe histogram exactly once")
			} else {
				assert.Equal(t, 0, spy.LoopDurationCount(),
					"non-compensated paths (including already_compensated and path_c_skipped no-op successes) MUST NOT observe histogram — would skew p50/p99 to floor")
			}
		})
	}
}

// TestHandleOrderFailed_UnmarshalError_RecordsOutcome — separate
// from the table-driven test above because unmarshal failures
// short-circuit BEFORE any UoW setup, so the mock chain is empty.
func TestHandleOrderFailed_UnmarshalError_RecordsOutcome(t *testing.T) {
	t.Parallel()
	comp, _, _, _, _, _, spy := compensatorHarnessWithSpy(t)

	err := comp.HandleOrderFailed(context.Background(), []byte("not json"), time.Now())
	require.Error(t, err)

	outcomes := spy.Outcomes()
	require.Len(t, outcomes, 1)
	assert.Equal(t, "unmarshal_error", outcomes[0])
	assert.Equal(t, 0, spy.LoopDurationCount(),
		"unmarshal failure observes no histogram — never reached the success path")
}
