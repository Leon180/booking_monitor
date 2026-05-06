package saga

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type Compensator interface {
	HandleOrderFailed(ctx context.Context, payload []byte) error
}

type compensator struct {
	inventoryRepo domain.InventoryRepository
	uow           application.UnitOfWork
	log           *mlog.Logger
}

// NewCompensator takes the logger as an explicit dependency rather
// than reaching for zap's globals, matching the pattern used by
// WorkerService and keeping tests deterministic (L7).
//
// All DB work happens inside uow.Do so the per-aggregate repos are
// resolved off the closure parameter; the Redis revert outside the
// tx still uses the long-lived inventoryRepo.
func NewCompensator(
	inventoryRepo domain.InventoryRepository,
	uow application.UnitOfWork,
	logger *mlog.Logger,
) Compensator {
	return &compensator{
		inventoryRepo: inventoryRepo,
		uow:           uow,
		log:           logger.With(mlog.String("component", "saga_compensator")),
	}
}

func (s *compensator) HandleOrderFailed(ctx context.Context, payload []byte) error {
	var event application.OrderFailedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		s.log.Error(ctx, "failed to unmarshal event",
			tag.Error(err),
			mlog.ByteString("payload", payload),
		)
		return fmt.Errorf("compensator.HandleOrderFailed unmarshal: %w", err)
	}

	s.log.Info(ctx, "rolling back inventory for failed order",
		tag.OrderID(event.OrderID),
		tag.EventID(event.EventID),
		tag.TicketTypeID(event.TicketTypeID),
		tag.Quantity(event.Quantity),
	)

	// 1. Rollback PostgreSQL Inventory & Ensure Idempotency via UoW
	ticketTypeIDForRevert := event.TicketTypeID
	errUow := s.uow.Do(ctx, func(repos *application.Repositories) error {
		order, err := repos.Order.GetByID(ctx, event.OrderID)
		if err != nil {
			return fmt.Errorf("orderRepo.GetByID order_id=%s: %w", event.OrderID, err)
		}
		if order.Status() == domain.OrderStatusCompensated {
			// Idempotent DB-side short-circuit. We still need a resolved
			// ticket_type_id for the Redis revert below: a previous
			// delivery may have marked the order compensated and then
			// failed on RevertInventory. Retrying only the Redis side is
			// safe because revert.lua is idempotent on compensationID.
			//
			// For modern payloads event.TicketTypeID is already set; for
			// legacy rolling-upgrade payloads we must re-run the fallback
			// resolution so ticketTypeIDForRevert is non-zero.
			ticketTypeID, err := s.resolveTicketTypeID(ctx, repos, event)
			if err != nil {
				return err
			}
			ticketTypeIDForRevert = ticketTypeID
			return nil
		}

		// D4.1 follow-up: increment the per-ticket-type counter (the
		// SoT the API surfaces). The legacy events.available_tickets
		// is frozen post-D4.1; its IncrementTicket is preserved for
		// backward compat but not called here.
		//
		// Resolve which ticket_type to increment via three paths:
		//
		//   Path A — clean (post-v3 wire format):
		//     event.TicketTypeID is non-nil; use directly.
		//
		//   Path B — legacy fallback (pre-v3 message in flight during
		//     rolling upgrade):
		//     event.TicketTypeID == uuid.Nil. Look up ticket_types for
		//     the event_id; D4.1's default-single-ticket-type-per-event
		//     pattern means exactly one row in 99% of cases. Use that
		//     row's id.
		//
		//   Path C — unrecoverable (D8 multi-ticket-type future + a
		//     legacy message somehow still in flight):
		//     event.TicketTypeID == uuid.Nil AND > 1 ticket_type per
		//     event. Skipping the DB increment is the conservative
		//     choice — wrong row means corrupting the visible counter,
		//     no row means manual review (the order's `failed` status
		//     is preserved so ops can find it). The Redis revert below
		//     also skips because the runtime key is ticket_type scoped.
		ticketTypeID, err := s.resolveTicketTypeID(ctx, repos, event)
		if err != nil {
			return err // already wrapped + logged inside resolveTicketTypeID
		}
		ticketTypeIDForRevert = ticketTypeID
		if ticketTypeID != uuid.Nil {
			if err := repos.TicketType.IncrementTicket(ctx, ticketTypeID, event.Quantity); err != nil {
				return fmt.Errorf("ticketTypeRepo.IncrementTicket ticket_type=%s: %w", ticketTypeID, err)
			}
		}
		// else: Path C skipped DB increment — Redis revert below still
		// runs. MarkCompensated still applies because the saga has
		// completed its user-visible work (Redis inventory is
		// authoritative for the read path; the DB ticket_type counter
		// drift is operationally visible and can be reconciled out-of-
		// band by ops).

		if err := repos.Order.MarkCompensated(ctx, event.OrderID); err != nil {
			return fmt.Errorf("orderRepo.MarkCompensated order_id=%s: %w", event.OrderID, err)
		}
		return nil
	})

	if errUow != nil {
		s.log.Error(ctx, "failed to rollback DB inventory", tag.OrderID(event.OrderID), tag.Error(errUow))
		return errUow // Retry — already wrapped inside the closure
	}

	// 2. Rollback Redis Inventory (Hot path). Idempotency is enforced
	// by the Lua script via an EXISTS-then-SET guard on a compensation
	// key (see revert.lua for the crash-safety trade-off).
	compensationID := fmt.Sprintf("order:%s", event.OrderID)
	if ticketTypeIDForRevert == uuid.Nil {
		s.log.Warn(ctx, "skipping Redis inventory revert because ticket_type_id is unavailable",
			tag.OrderID(event.OrderID),
			tag.EventID(event.EventID),
		)
		return nil
	}
	if err := s.inventoryRepo.RevertInventory(ctx, ticketTypeIDForRevert, event.Quantity, compensationID); err != nil {
		s.log.Error(ctx, "failed to rollback Redis inventory",
			tag.OrderID(event.OrderID),
			tag.Error(err),
		)
		return fmt.Errorf("inventoryRepo.RevertInventory order_id=%s: %w", event.OrderID, err)
	}

	s.log.Info(ctx, "rollback successful", tag.OrderID(event.OrderID))
	return nil
}

// resolveTicketTypeID maps the OrderFailedEvent to the ticket_type that
// IncrementTicket should target, per the three-path resolution
// documented in HandleOrderFailed:
//
//   - Path A (clean):       event.TicketTypeID != uuid.Nil → return it.
//   - Path B (legacy):      ListByEventID returns exactly 1 row → use it.
//                           Logs Warn so ops see the rolling-upgrade window.
//   - Path C (unrecoverable): ListByEventID returns 0 or > 1 rows → return
//                           (uuid.Nil, nil). Caller skips DB increment but
//                           continues to Redis revert + MarkCompensated.
//                           Logs Error so the case is operator-visible.
//
// ListByEventID lookup errors propagate up wrapped.
func (s *compensator) resolveTicketTypeID(ctx context.Context, repos *application.Repositories, event application.OrderFailedEvent) (uuid.UUID, error) {
	if event.TicketTypeID != uuid.Nil {
		return event.TicketTypeID, nil
	}

	// Legacy event (wire format v < 3) caught in Kafka during rolling
	// upgrade. Try a per-event lookup as a recovery path.
	ticketTypes, err := repos.TicketType.ListByEventID(ctx, event.EventID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("ticketTypeRepo.ListByEventID event_id=%s (legacy event fallback): %w", event.EventID, err)
	}
	switch len(ticketTypes) {
	case 1:
		// D4.1 default-single-ticket-type-per-event — the dominant
		// case during rolling upgrade. Safe to attribute the
		// compensation to this row.
		s.log.Warn(ctx, "saga compensator: legacy event missing ticket_type_id; resolved via single-ticket-type fallback (rolling-upgrade window)",
			tag.OrderID(event.OrderID),
			tag.EventID(event.EventID),
			tag.TicketTypeID(ticketTypes[0].ID()),
		)
		return ticketTypes[0].ID(), nil
	case 0:
		// Event has no ticket_types — data corruption (event was
		// created pre-D4.1 and never migrated). Manual review needed.
		s.log.Error(ctx, "saga compensator: legacy event missing ticket_type_id AND no ticket_types found for event; DB ticket_type increment skipped — manual review required",
			tag.OrderID(event.OrderID),
			tag.EventID(event.EventID),
		)
		return uuid.Nil, nil
	default:
		// > 1 ticket_type — D8 multi-ticket-type expansion + a legacy
		// pre-v3 message still in Kafka. We CANNOT pick the right one
		// without the explicit TicketTypeID. Skip rather than corrupt.
		s.log.Error(ctx, "saga compensator: legacy event missing ticket_type_id AND multiple ticket_types per event; cannot disambiguate, DB ticket_type increment skipped — manual review required",
			tag.OrderID(event.OrderID),
			tag.EventID(event.EventID),
			mlog.Int("ticket_type_count", len(ticketTypes)),
		)
		return uuid.Nil, nil
	}
}
