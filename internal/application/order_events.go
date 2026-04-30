package application

import (
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// OrderCreatedEvent / OrderFailedEvent + their factories live here, in
// application, NOT in domain. They are wire-format DTOs published to
// Kafka (`order.created` / `order.failed`) — application owns that
// transport contract. Putting them in domain would smuggle JSON wire
// concerns into entity definitions (the rule-7 violation closed by
// PR #38). Mirrors the OrderQueue / QueuedBookingMessage relocation
// from PR #37.
//
// json tags ARE legitimate here: the types are DTOs by purpose (no
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
const OrderEventVersion = 2

// OrderCreatedEvent is the wire-format payload published to the
// Kafka `order.created` topic and consumed by the payment service.
// Defined separately from `domain.Order` so the messaging contract
// can evolve independently of the domain model — adding a domain
// field no longer leaks into the wire format unless the mapper
// (NewOrderCreatedEvent) explicitly copies it across.
//
// Field-tag history:
//   - `OrderID json:"id"` — kept stable; was already "id" in v1.
//   - `Amount` — pre-existing field expected by the consumer for
//     gateway.Charge but the producer-side mapper sets it to 0 today
//     because pricing isn't modelled in domain.Order. Tracked as a
//     pre-existing semantic gap; out of scope for the rule-7 audit.
//   - `Version` — bumped to 2 with the UUID migration.
type OrderCreatedEvent struct {
	OrderID   uuid.UUID `json:"id"`
	Status    string    `json:"status"`
	UserID    int       `json:"user_id"`
	EventID   uuid.UUID `json:"event_id"`
	Quantity  int       `json:"quantity"`
	Amount    float64   `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
	Version   int       `json:"version"`
}

// OrderFailedEvent is the wire-format payload published to
// `order.failed` (saga compensation trigger).
type OrderFailedEvent struct {
	EventID  uuid.UUID `json:"event_id"`
	OrderID  uuid.UUID `json:"order_id"`
	UserID   int       `json:"user_id"`
	Quantity int       `json:"quantity"`
	FailedAt time.Time `json:"failed_at"`
	Reason   string    `json:"reason"`
	Version  int       `json:"version"`
}

// NewOrderCreatedEvent translates a domain.Order to the producer-side
// wire payload. The single seam where domain shape meets messaging
// shape — adding a domain field that consumers shouldn't see is now
// a no-op; surfacing one is an explicit edit here.
//
// Amount is set to 0 because domain.Order has no pricing field today.
// See the package-level note above.
func NewOrderCreatedEvent(o domain.Order) OrderCreatedEvent {
	return OrderCreatedEvent{
		OrderID:   o.ID(),
		Status:    string(o.Status()),
		UserID:    o.UserID(),
		EventID:   o.EventID(),
		Quantity:  o.Quantity(),
		Amount:    0,
		CreatedAt: o.CreatedAt(),
		Version:   OrderEventVersion,
	}
}

// NewOrderFailedEvent translates a saga-compensation trigger from the
// payment service's incoming event + a failure reason. Takes
// OrderCreatedEvent (not Order) because the payment service consumes
// this from Kafka — it doesn't have a fresh domain.Order at hand,
// only the prior event payload.
func NewOrderFailedEvent(from OrderCreatedEvent, reason string) OrderFailedEvent {
	return OrderFailedEvent{
		EventID:  from.EventID,
		OrderID:  from.OrderID,
		UserID:   from.UserID,
		Quantity: from.Quantity,
		FailedAt: time.Now(),
		Reason:   reason,
		Version:  OrderEventVersion,
	}
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
		EventID:  o.EventID(),
		OrderID:  o.ID(),
		UserID:   o.UserID(),
		Quantity: o.Quantity(),
		FailedAt: time.Now(),
		Reason:   reason,
		Version:  OrderEventVersion,
	}
}
