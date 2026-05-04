package event

import (
	"context"
	"fmt"

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
	//   - Redis error                   wrapped + compensation triggered
	//   - "compensation failed"         dangling row scenario; both errors
	//                                   surfaced for manual recon
	CreateEvent(ctx context.Context, name string, totalTickets int, priceCents int64, currency string) (CreateEventResult, error)
}

type service struct {
	uow            application.UnitOfWork
	eventRepo      domain.EventRepository
	ticketTypeRepo domain.TicketTypeRepository
	inventoryRepo  domain.InventoryRepository
	log            *mlog.Logger
}

// NewService takes the logger as an explicit dependency rather than
// reaching for zap's globals. This matches WorkerService /
// SagaCompensator and guarantees the logger is initialized by the time
// fx calls this constructor.
//
// D4.1 adds the UoW + TicketTypeRepository for the atomic
// event-plus-default-ticket-type create flow. The non-tx eventRepo
// and ticketTypeRepo are still held separately because the
// compensation path runs OUTSIDE the original tx (the Redis call
// happens after commit, so a SetInventory failure can't be rolled
// back via the same UoW). For compensation we open a second UoW Do
// to delete both rows atomically.
func NewService(
	uow application.UnitOfWork,
	eventRepo domain.EventRepository,
	ticketTypeRepo domain.TicketTypeRepository,
	inventoryRepo domain.InventoryRepository,
	logger *mlog.Logger,
) Service {
	return &service{
		uow:            uow,
		eventRepo:      eventRepo,
		ticketTypeRepo: ticketTypeRepo,
		inventoryRepo:  inventoryRepo,
		log:            logger.With(mlog.String("component", "event_service")),
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
	var createdEvent domain.Event
	var createdTT domain.TicketType
	err = s.uow.Do(ctx, func(repos *application.Repositories) error {
		createdEvent, err = repos.Event.Create(ctx, ev)
		if err != nil {
			return err
		}
		createdTT, err = repos.TicketType.Create(ctx, tt)
		if err != nil {
			return err
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
	if err := s.inventoryRepo.SetInventory(ctx, createdEvent.ID(), totalTickets); err != nil {
		compensateErr := s.compensateDanglingEvent(ctx, createdEvent.ID(), createdTT.ID())
		if compensateErr != nil {
			// Compensation failed — both rows still exist in DB with
			// no Redis inventory. Surface BOTH errors so an operator
			// can manually clean up.
			s.log.Error(ctx, "COMPENSATION FAILED — dangling event + ticket_type rows",
				tag.EventID(createdEvent.ID()),
				mlog.NamedError("redis_error", err),
				mlog.NamedError("compensation_error", compensateErr),
			)
			return CreateEventResult{}, fmt.Errorf("service.CreateEvent: redis SetInventory failed (%v) AND compensating delete failed: %w", err, compensateErr)
		}
		s.log.Warn(ctx, "compensated dangling event + ticket_type after Redis failure",
			tag.EventID(createdEvent.ID()), tag.Error(err))
		return CreateEventResult{}, fmt.Errorf("service.CreateEvent redis SetInventory: %w", err)
	}

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
