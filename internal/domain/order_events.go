package domain

import "time"

// Wire-format schema version for the order.* event family. Bumped only
// when a backward-incompatible change is made (field removal, type
// change, semantic redefinition); additive changes (new optional
// fields) keep the same version. Consumers SHOULD branch on this
// value when they need to support both old and new shapes during a
// rolling migration.
const OrderEventVersion = 1

// OrderCreatedEvent is the wire-format payload published to the
// Kafka order.created topic and consumed by the payment service.
// Defined separately from domain.Order so the messaging contract can
// evolve independently of the domain model — adding a domain field
// no longer leaks into the wire format unless the mapper
// (NewOrderCreatedEvent) explicitly copies it across.
//
// Field-tag history:
//   - `OrderID json:"id"` — deliberately "id", not "order_id", to
//     match the existing producer output (domain.Order.ID had
//     `json:"id"` before PR 32). Kept stable so this PR doesn't break
//     in-flight messages from any rolling-deployed consumer.
//   - `Amount` — pre-existing field expected by the consumer for
//     gateway.Charge but the producer-side mapper sets it to 0 today
//     because pricing isn't modelled in domain.Order. Tracked as a
//     pre-existing semantic gap (see commit message); fixing it
//     requires introducing an event Pricing model and is out of scope
//     for PR 32.
//   - `Version` — added in PR 32, default OrderEventVersion. New
//     producer always emits; old messages still in flight will
//     unmarshal with Version=0 and consumers can detect that.
type OrderCreatedEvent struct {
	OrderID   int       `json:"id"`
	Status    string    `json:"status"`
	UserID    int       `json:"user_id"`
	EventID   int       `json:"event_id"`
	Quantity  int       `json:"quantity"`
	Amount    float64   `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
	Version   int       `json:"version"`
}

// OrderFailedEvent is the wire-format payload published to
// order.failed (saga compensation trigger).
type OrderFailedEvent struct {
	EventID  int       `json:"event_id"`
	OrderID  int       `json:"order_id"`
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
func NewOrderCreatedEvent(o Order) OrderCreatedEvent {
	return OrderCreatedEvent{
		OrderID:   o.ID,
		Status:    string(o.Status),
		UserID:    o.UserID,
		EventID:   o.EventID,
		Quantity:  o.Quantity,
		Amount:    0,
		CreatedAt: o.CreatedAt,
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
