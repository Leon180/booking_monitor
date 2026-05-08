package application

import (
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// OrderFailedEvent + its factory live here, in application, NOT in
// domain. It is a wire-format DTO published to Kafka (`order.failed`)
// — application owns that transport contract. Putting it in domain
// would smuggle JSON wire concerns into entity definitions (the rule-7
// violation closed by PR #38). Mirrors the OrderQueue /
// QueuedBookingMessage relocation from PR #37.
//
// json tags ARE legitimate here: the type is a DTO by purpose (no
// invariants enforced, no behaviour, just shape), unlike domain
// entities which the rule keeps tag-free.

// OrderEventVersion is the wire-format schema version for the
// `order.*` event family. Bumped only when a backward-incompatible
// change is made (field removal, type change, semantic redefinition);
// additive changes (new optional fields) keep the same version.
// Consumers SHOULD branch on this value when they need to support
// both old and new shapes during a rolling migration.
//
// Version history:
//   - v1 (pre PR 34): int IDs (SERIAL).
//   - v2 (PR 34):     IDs migrated from int to UUID v7. Producer
//     serialises IDs as RFC 4122 strings; consumers that expected int
//     fail to unmarshal — coordinate the bump.
//   - v3 (D4.1 follow-up): adds `ticket_type_id` (uuid) to
//     OrderFailedEvent so the saga compensator can route
//     IncrementTicket calls to the correct event_ticket_types row
//     instead of the legacy events.available_tickets column. The field
//     is additive — old consumers that ignore unknown JSON fields keep
//     working — but the version bump signals to forward-compatible
//     consumers (e.g. the saga compensator's legacy-fallback branch)
//     that the field IS expected to be present, so a uuid.Nil value is
//     a recovery-required signal rather than a normal pre-D4.1 message.
//   - **D7 (2026-05-08)**: `OrderCreatedEvent` was removed entirely
//     (deleted with the legacy A4 auto-charge path). The wire-format
//     family now contains only `OrderFailedEvent`. The saga-trigger
//     factories `NewOrderFailedEvent(from OrderCreatedEvent, ...)`
//     was deleted in the same PR; callers use
//     `NewOrderFailedEventFromOrder(domain.Order, ...)` exclusively.
const OrderEventVersion = 3

// OrderFailedEvent is the wire-format payload published to
// `order.failed` (saga compensation trigger).
//
// `TicketTypeID` is required by the D4.1+ saga compensator to drive
// `TicketTypeRepository.IncrementTicket` against the correct
// event_ticket_types row. A `uuid.Nil` value indicates the producer
// emitted a pre-v3 (legacy) event still in flight on Kafka during a
// rolling upgrade; the compensator falls back to a per-event lookup
// (ListByEventID, single-ticket-type case) before logging a recovery
// error. See `saga.compensator.HandleOrderFailed` for the three-path
// resolution.
type OrderFailedEvent struct {
	EventID      uuid.UUID `json:"event_id"`
	OrderID      uuid.UUID `json:"order_id"`
	UserID       int       `json:"user_id"`
	TicketTypeID uuid.UUID `json:"ticket_type_id"`
	Quantity     int       `json:"quantity"`
	FailedAt     time.Time `json:"failed_at"`
	Reason       string    `json:"reason"`
	Version      int       `json:"version"`
}

// NewOrderFailedEventFromOrder is the recon / saga-watchdog entry
// point: callers that already loaded a domain.Order (via GetByID or
// FindStuckCharging / FindStuckFailed) should use this instead of
// synthesising a throwaway OrderCreatedEvent. Maps directly off the
// aggregate so wire fields like Version are filled correctly — the
// previous shape (NewOrderFailedEvent(OrderCreatedEvent{...partial},
// reason)) zeroed Version and other fields, producing a wire-format
// schema violation.
func NewOrderFailedEventFromOrder(o domain.Order, reason string) OrderFailedEvent {
	return OrderFailedEvent{
		EventID:      o.EventID(),
		OrderID:      o.ID(),
		UserID:       o.UserID(),
		TicketTypeID: o.TicketTypeID(),
		Quantity:     o.Quantity(),
		FailedAt:     time.Now(),
		Reason:       reason,
		Version:      OrderEventVersion,
	}
}
