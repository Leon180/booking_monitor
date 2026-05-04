package event

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// defaultTicketTypeName is the label given to the auto-created
// ticket_type when a new event is provisioned. KKTIX-style admin UIs
// would let the operator name each 票種 (VIP, 一般, 學生 etc.) at
// creation time; D4.1 ships a single default ticket_type per event so
// the existing API surface (POST /events without explicit ticket_type
// configuration) keeps working. D8 will lift this when admin can post
// `[{name, price_cents, total}, ...]` in the request body.
const defaultTicketTypeName = "Default"

// compensationBudget caps the time the compensation tx is allowed to
// run when the request ctx is already cancelled / past-deadline. The
// ctx the handler hands in may have expired (HTTP client disconnected
// during SetInventory), and using it for compensation would fail at
// BeginTx with `context.Canceled` before the tx even opens — leaving
// dangling rows. Same pattern as `cache/redis_queue.go::handleFailure`
// using a background ctx with `q.failureTimeout`. 5s mirrors the
// production WORKER_FAILURE_TIMEOUT default.
const compensationBudget = 5 * time.Second

// CreateEventResult is the return shape of CreateEvent. Wraps the
// created event together with the ticket types that were provisioned
// alongside it. D4.1 always returns exactly one ticket_type (the
// default); D8 will return whatever the admin payload specified.
//
// Returned as a struct rather than a `(Event, []TicketType, error)`
// triple so adding a future field (e.g. DefaultTicketTypeID for the
// caller's convenience) doesn't break every call site.
type CreateEventResult struct {
	Event       domain.Event
	TicketTypes []domain.TicketType
}

type Service interface {
	// CreateEvent atomically provisions a new event and a default
	// ticket_type carrying the (priceCents, currency) snapshot.
	//
	// D4.1 contract:
	//   1. Validate domain entities (NewEvent + NewTicketType
	//      invariants) BEFORE any I/O.
	//   2. UoW: INSERT events + event_ticket_types in one transaction
	//      so the "event with no ticket_type" partial state is
	//      structurally impossible.
	//   3. Redis SetInventory (best-effort with compensation: on Redis
	//      failure, delete BOTH rows so a retry can re-Create cleanly).
	//
	// Errors:
	//   - domain.ErrInvalid*           invariant failure (no I/O performed)
	//   - tx error                      DB unreachable / constraint violation
	//   - domain.ErrTicketTypeNameTaken duplicate (event_id, name) — surfaced
	//                                   from the UoW unique-constraint branch
	//   - Redis error                   wrapped + compensation triggered
	//   - "compensation failed"         dangling row scenario; both errors
	//                                   joined for manual recon
	CreateEvent(ctx context.Context, name string, totalTickets int, priceCents int64, currency string) (CreateEventResult, error)
}

type service struct {
	uow           application.UnitOfWork
	inventoryRepo domain.InventoryRepository
	log           *mlog.Logger
}

// NewService takes the logger as an explicit dependency rather than
// reaching for zap's globals. This matches WorkerService /
// SagaCompensator and guarantees the logger is initialized by the time
// fx calls this constructor.
//
// D4.1 NOTE: pre-bfe3587 this constructor also accepted standalone
// non-tx EventRepository + TicketTypeRepository fields. They were
// removed because every D4.1 access goes through the UoW (`repos.Event`
// / `repos.TicketType` inside the closure) — the standalone fields
// were dead injection. Keeping them was a maintenance trap: a future
// reader might call `s.eventRepo.Delete` outside a tx in compensation,
// bypassing the atomicity that compensateDanglingEvent enforces.
func NewService(
	uow application.UnitOfWork,
	inventoryRepo domain.InventoryRepository,
	logger *mlog.Logger,
) Service {
	return &service{
		uow:           uow,
		inventoryRepo: inventoryRepo,
		log:           logger.With(mlog.String("component", "event_service")),
	}
}

func (s *service) CreateEvent(ctx context.Context, name string, totalTickets int, priceCents int64, currency string) (CreateEventResult, error) {
	// 1. Validate event invariants.
	ev, err := domain.NewEvent(name, totalTickets)
	if err != nil {
		return CreateEventResult{}, err
	}

	// 2. Build the default ticket_type. ID is caller-generated per the
	//    project's §8 caller-generated-id convention.
	ttID, err := uuid.NewV7()
	if err != nil {
		return CreateEventResult{}, fmt.Errorf("generate ticket_type id: %w", err)
	}
	tt, err := domain.NewTicketType(
		ttID, ev.ID(), defaultTicketTypeName, priceCents, currency, totalTickets,
		nil, nil, nil, "", // sale window / per-user limit / area_label all unset for the default
	)
	if err != nil {
		return CreateEventResult{}, err
	}

	// 3. Persist both atomically inside a single tx. Either both rows
	//    land or neither does — the "event without a ticket_type"
	//    partial state is structurally impossible.
	//
	//    Using a shadow `innerErr` inside the closure (NOT writing to
	//    the outer `err`) so a future UoW that retries the closure on
	//    deadlock doesn't replay an outer-scope assignment that's
	//    surprising to read.
	var createdEvent domain.Event
	var createdTT domain.TicketType
	err = s.uow.Do(ctx, func(repos *application.Repositories) error {
		var innerErr error
		createdEvent, innerErr = repos.Event.Create(ctx, ev)
		if innerErr != nil {
			return innerErr
		}
		createdTT, innerErr = repos.TicketType.Create(ctx, tt)
		if innerErr != nil {
			return innerErr
		}
		return nil
	})
	if err != nil {
		return CreateEventResult{}, fmt.Errorf("service.CreateEvent uow: %w", err)
	}

	// 4. Redis SetInventory. If it fails after the tx committed, the
	//    booking hot path would read sold-out for an event that has
	//    DB-side inventory — permanently unsellable. Compensate by
	//    deleting BOTH rows in a second UoW so the operator (or an
	//    automatic retry) can re-Create the event cleanly.
	//
	//    Recovery cross-reference: if the worker SIGKILLs between this
	//    UoW commit and the SetInventory call, neither compensation
	//    nor the SetInventory ever runs — but `cache/rehydrate.go`
	//    runs at app startup, queries `EventRepository.ListAvailable`,
	//    and SetNX-installs Redis qty keys for any event whose Redis
	//    key is absent. So a pod restart (or its k8s liveness probe
	//    equivalent) is a safety net for the SIGKILL-mid-flight case.
	//    Manual cleanup is only needed when the Redis fail + compensation
	//    fail BOTH happen and no pod restart follows.
	if err := s.inventoryRepo.SetInventory(ctx, createdEvent.ID(), totalTickets); err != nil {
		// Compensation runs against a fresh, detached background
		// context. The handler ctx may have already been cancelled
		// (HTTP client timeout was the cause of the SetInventory
		// failure), and threading that ctx through to BeginTx would
		// fail immediately with context.Canceled — leaving dangling
		// rows. The 5s budget mirrors WORKER_FAILURE_TIMEOUT.
		compensateCtx, cancel := context.WithTimeout(context.Background(), compensationBudget)
		defer cancel()
		compensateErr := s.compensateDanglingEvent(compensateCtx, createdEvent.ID(), createdTT.ID())
		if compensateErr != nil {
			// Compensation failed — both rows still exist in DB with
			// no Redis inventory. Surface BOTH errors via errors.Join
			// so callers can `errors.Is` against the Redis sentinel
			// AND the compensation sentinel from the same chain.
			s.log.Error(ctx, "COMPENSATION FAILED — dangling event + ticket_type rows",
				tag.EventID(createdEvent.ID()),
				tag.TicketTypeID(createdTT.ID()),
				mlog.NamedError("redis_error", err),
				mlog.NamedError("compensation_error", compensateErr),
			)
			return CreateEventResult{}, fmt.Errorf("service.CreateEvent: redis SetInventory + compensation both failed: %w", errors.Join(err, compensateErr))
		}
		// Standardised log key `redis_error` so an operator running
		// `redis_error=<pattern>` finds both compensated AND
		// dangling-row events in one query. Don't switch to the
		// generic `tag.Error` here — that'd put the same value under
		// the key "error" and split the two outcome paths across
		// two log queries.
		s.log.Warn(ctx, "compensated dangling event + ticket_type after Redis failure",
			tag.EventID(createdEvent.ID()),
			tag.TicketTypeID(createdTT.ID()),
			mlog.NamedError("redis_error", err),
		)
		return CreateEventResult{}, fmt.Errorf("service.CreateEvent redis SetInventory: %w", err)
	}

	// Success path log — emits the (event_id, ticket_type_id, name)
	// triple so post-deployment verification can grep for the
	// auto-provisioned default ticket_type by event id.
	s.log.Info(ctx, "created event with default ticket_type",
		tag.EventID(createdEvent.ID()),
		tag.TicketTypeID(createdTT.ID()),
		mlog.String("ticket_type_name", defaultTicketTypeName),
	)

	return CreateEventResult{
		Event:       createdEvent,
		TicketTypes: []domain.TicketType{createdTT},
	}, nil
}

// compensateDanglingEvent deletes the event + its default ticket_type
// in a single transaction. Called when a post-commit Redis SetInventory
// fails — the goal is to leave DB clean so a retry of CreateEvent can
// proceed without colliding with the orphan rows.
//
// Both deletes are idempotent (DELETE on a missing row is a no-op),
// so a partial earlier compensation can re-run safely.
//
// The ctx passed in is detached from the caller's request ctx — see
// CreateEvent's call site for why. Compensation budget is enforced by
// the caller's `context.WithTimeout`, not by this function.
func (s *service) compensateDanglingEvent(ctx context.Context, eventID, ticketTypeID uuid.UUID) error {
	return s.uow.Do(ctx, func(repos *application.Repositories) error {
		if err := repos.TicketType.Delete(ctx, ticketTypeID); err != nil {
			return fmt.Errorf("delete ticket_type %s: %w", ticketTypeID, err)
		}
		if err := repos.Event.Delete(ctx, eventID); err != nil {
			return fmt.Errorf("delete event %s: %w", eventID, err)
		}
		return nil
	})
}
