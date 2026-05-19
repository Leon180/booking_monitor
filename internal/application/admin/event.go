// Package admin contains the wire-format event types and bus interface
// for the admin event streaming layer (SSE to ops dashboard).
//
// This sub-package is the application-layer contract — wire-format DTOs
// for events published through the admin event bus. Following the
// codebase rule (see `internal/application/order_events.go` § rule-7):
// JSON-tagged wire DTOs live in application, NOT domain. Domain stays
// tag-free; application owns transport contracts.
//
// The bus implementation (Redis Streams XADD with bounded-async + drop
// policy) lives in `internal/infrastructure/cache/admin_event_bus.go`
// so this package has no Redis dependency. Callers depend on the Bus
// interface, fx wires the concrete impl.
//
// Design doc: docs/design/admin_event_streaming.md (Q4)
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AdminEventSchemaVersion is the wire-format envelope version.
//
// Versioning policy:
//   - additive changes (new optional fields in Data payload, new event
//     types): same version
//   - breaking changes (envelope field rename/removal, type change):
//     bump version, consumers branch on AdminEvent.SchemaVersion
//
// v1 (2026-05-20): initial release.
const AdminEventSchemaVersion = 1

// Event type discriminator constants. These map 1:1 to the SSE wire
// `event:` line and dashboard filtering. Add new constants here when
// extending — DO NOT spell strings inline at call sites.
const (
	EventTypeOrderCreated     = "order.created"
	EventTypeOrderPaid        = "order.paid"
	EventTypeOrderFailed      = "order.failed"
	EventTypeOrderExpired     = "order.expired"
	EventTypeOrderCompensated = "order.compensated"
	EventTypeSagaTriggered    = "saga.triggered"
	EventTypeDLQReceived      = "dlq.received"
	EventTypeInventoryLow     = "inventory.low"
)

// AdminEvent is the wire envelope sent through the admin event bus.
// All 8 admin event types share this shape; per-type details live in
// the Data field as a typed payload struct marshaled to JSON.
//
// The two ID fields serve different purposes:
//   - EventID (UUIDv7): globally unique event identifier, used for
//     app-level dedup and trace correlation. Carried in payload.
//   - SSE wire `id:` line: Redis Stream ID (assigned by Redis on XADD),
//     used as Last-Event-ID resumption token. See design doc Q6.
//
// EventID + Stream ID coexist — different layers, different jobs.
type AdminEvent struct {
	EventID       uuid.UUID       `json:"event_id"`
	EventType     string          `json:"event_type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	SchemaVersion int             `json:"schema_version"`
	Data          json.RawMessage `json:"data"`
}

// Per-type payload structs. Each is marshaled into AdminEvent.Data by
// the factory functions below. Defining typed structs (rather than
// map[string]any) preserves compile-time field checks at publish sites.

// OrderLifecyclePayload carries data for the five order lifecycle
// event types: order.created / order.paid / order.failed /
// order.expired / order.compensated. The EventType envelope field
// discriminates; FromStatus is empty for order.created (no prior state).
type OrderLifecyclePayload struct {
	OrderID      uuid.UUID `json:"order_id"`
	UserID       int       `json:"user_id"`
	TicketTypeID uuid.UUID `json:"ticket_type_id"`
	Quantity     int       `json:"quantity"`
	FromStatus   string    `json:"from_status,omitempty"`
	ToStatus     string    `json:"to_status"`
}

// SagaTriggeredPayload signals the START of a compensation flow
// (NOT a state transition). Per design doc Q5 discipline #3, this
// event is intentionally emitted before the compensation actually
// completes — `compensation_id` lets correlate with the eventual
// order.compensated event.
type SagaTriggeredPayload struct {
	OrderID        uuid.UUID `json:"order_id"`
	CompensationID string    `json:"compensation_id"`
	Reason         string    `json:"reason"`
}

// DLQReceivedPayload describes a message moved to the Redis DLQ
// stream. OrderID is omitempty because malformed messages may not
// have a parseable order_id; in that case Reason contains the parse
// error detail.
type DLQReceivedPayload struct {
	OrderID  uuid.UUID `json:"order_id,omitempty"`
	Reason   string    `json:"reason"`
	Attempts int       `json:"attempts"`
}

// InventoryLowPayload fires when a ticket_type's available count
// crosses the low-stock threshold (configurable, default 10%).
// ThresholdPct is the configured threshold the alert tripped on
// (informational; ops sees "we crossed below 10%").
type InventoryLowPayload struct {
	TicketTypeID uuid.UUID `json:"ticket_type_id"`
	Available    int       `json:"available"`
	Total        int       `json:"total"`
	ThresholdPct float64   `json:"threshold_pct"`
}

// Factory sentinel errors. Callers can match with errors.Is to branch
// on validation failure. The factories themselves are infallible for
// "normal" inputs — these errors fire only when programmer error or
// data corruption produces obviously-wrong values (e.g., uuid.Nil).
var (
	ErrInvalidOrderID      = errors.New("admin event: order_id must not be nil")
	ErrInvalidTicketTypeID = errors.New("admin event: ticket_type_id must not be nil")
	ErrInvalidEventType    = errors.New("admin event: event_type must be one of the known constants")
	ErrEmptyReason         = errors.New("admin event: reason must not be empty")
)

// newEnvelope is the shared factory step: mints a UUIDv7 EventID,
// stamps OccurredAt = time.Now().UTC(), marshals the typed payload,
// and returns a ready-to-publish AdminEvent.
//
// Returning value (not pointer) follows the codebase convention from
// `.claude/rules/golang/coding-style.md` "Factories return values".
func newEnvelope(eventType string, payload any) (AdminEvent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return AdminEvent{}, fmt.Errorf("generate admin event id: %w", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return AdminEvent{}, fmt.Errorf("marshal admin event payload: %w", err)
	}
	return AdminEvent{
		EventID:       id,
		EventType:     eventType,
		OccurredAt:    time.Now().UTC(),
		SchemaVersion: AdminEventSchemaVersion,
		Data:          data,
	}, nil
}

// NewOrderLifecycleEvent constructs one of the five order lifecycle
// events. The caller passes the eventType constant explicitly so the
// transition (FromStatus → ToStatus) is captured at the publishing
// call site rather than inferred from the order's current state.
//
// Example:
//
//	evt, err := admin.NewOrderLifecycleEvent(
//	    admin.EventTypeOrderPaid,
//	    admin.OrderLifecyclePayload{
//	        OrderID:      order.ID(),
//	        UserID:       order.UserID(),
//	        TicketTypeID: order.TicketTypeID(),
//	        Quantity:     order.Quantity(),
//	        FromStatus:   "awaiting_payment",
//	        ToStatus:     "paid",
//	    },
//	)
func NewOrderLifecycleEvent(eventType string, p OrderLifecyclePayload) (AdminEvent, error) {
	if !isOrderLifecycleType(eventType) {
		return AdminEvent{}, fmt.Errorf("%w: got %q", ErrInvalidEventType, eventType)
	}
	if p.OrderID == uuid.Nil {
		return AdminEvent{}, ErrInvalidOrderID
	}
	if p.TicketTypeID == uuid.Nil {
		return AdminEvent{}, ErrInvalidTicketTypeID
	}
	return newEnvelope(eventType, p)
}

// NewSagaTriggeredEvent emits a saga.triggered signal. Per the design
// doc Q5 discipline #3, this fires at the START of HandleOrderFailed,
// before compensation actually runs — the matching order.compensated
// event will fire (with same OrderID) once compensation completes.
func NewSagaTriggeredEvent(p SagaTriggeredPayload) (AdminEvent, error) {
	if p.OrderID == uuid.Nil {
		return AdminEvent{}, ErrInvalidOrderID
	}
	if p.Reason == "" {
		return AdminEvent{}, ErrEmptyReason
	}
	return newEnvelope(EventTypeSagaTriggered, p)
}

// NewDLQReceivedEvent fires when a message lands in the Redis DLQ
// stream. OrderID may be uuid.Nil when the original message was so
// malformed that order_id couldn't be parsed (typically captured in
// Reason as a parse error). The Nil case is the only context where
// uuid.Nil is acceptable on this event type.
func NewDLQReceivedEvent(p DLQReceivedPayload) (AdminEvent, error) {
	if p.Reason == "" {
		return AdminEvent{}, ErrEmptyReason
	}
	if p.Attempts < 0 {
		return AdminEvent{}, fmt.Errorf("admin event: attempts must be non-negative, got %d", p.Attempts)
	}
	return newEnvelope(EventTypeDLQReceived, p)
}

// NewInventoryLowEvent fires when a ticket_type's available count
// crosses the configured low-stock threshold. Available <= 0 is
// allowed (zero available = exactly sold out, still alert-worthy).
func NewInventoryLowEvent(p InventoryLowPayload) (AdminEvent, error) {
	if p.TicketTypeID == uuid.Nil {
		return AdminEvent{}, ErrInvalidTicketTypeID
	}
	if p.Total <= 0 {
		return AdminEvent{}, fmt.Errorf("admin event: total must be positive, got %d", p.Total)
	}
	if p.ThresholdPct < 0 || p.ThresholdPct > 1 {
		return AdminEvent{}, fmt.Errorf("admin event: threshold_pct must be in [0, 1], got %f", p.ThresholdPct)
	}
	return newEnvelope(EventTypeInventoryLow, p)
}

// isOrderLifecycleType returns true for the five EventType constants
// that share OrderLifecyclePayload. Kept private — callers should pass
// the named constants directly rather than discover what's valid.
func isOrderLifecycleType(t string) bool {
	switch t {
	case EventTypeOrderCreated,
		EventTypeOrderPaid,
		EventTypeOrderFailed,
		EventTypeOrderExpired,
		EventTypeOrderCompensated:
		return true
	}
	return false
}
