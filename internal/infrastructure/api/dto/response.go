package dto

import (
	"time"

	"github.com/google/uuid"
)

// OrderResponse is the wire shape of an order in API responses. It is
// deliberately distinct from domain.Order — fields and types here
// reflect the JSON contract clients expect, not the domain model
// shape, so the two evolve independently.
//
// uuid.UUID marshals as RFC 4122 string ("01900000-...") via its
// built-in MarshalJSON. Clients receive ID + EventID as strings.
type OrderResponse struct {
	ID        uuid.UUID `json:"id"`
	EventID   uuid.UUID `json:"event_id"`
	UserID    int       `json:"user_id"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// EventResponse is the wire shape of an event in API responses.
type EventResponse struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	TotalTickets     int       `json:"total_tickets"`
	AvailableTickets int       `json:"available_tickets"`
	Version          int       `json:"version"`
}

// ListBookingsResponse is the wire shape of GET /api/v1/history.
type ListBookingsResponse struct {
	Data []OrderResponse `json:"data"`
	Meta Meta            `json:"meta"`
}

// Meta is the pagination block shared by list responses.
type Meta struct {
	Total int `json:"total"`
	Page  int `json:"page"`
	Size  int `json:"size"`
}

// BookingAcceptedResponse is the wire shape returned by POST /api/v1/book
// on success. The 202 semantics are honest about the async pipeline:
// the Redis-side inventory deduct succeeded (the "gate"), an order
// intent has been queued, and the rest of the lifecycle (DB persist,
// payment charge, saga compensation) is in flight. Clients use the
// returned `OrderID` against `GET /api/v1/orders/:id` to track the
// terminal status.
//
// Why this shape:
//   - `OrderID` is the canonical correlation handle. Echoed in the
//     server logs (correlation_id), tracing span (order_id attr),
//     and DB orders.id. A customer reporting "my booking is missing"
//     hands over the order_id and operators have a single string to
//     pivot from.
//   - `Status` is the application-layer state at the moment of
//     response — `processing` means "Redis accepted, worker pipeline
//     in flight". Kept distinct from HTTP status so a future
//     synchronous-confirmation flow could return 200 + status:"confirmed".
//   - `Links.Self` follows the loose HAL/JSON:API convention so
//     clients can navigate without hard-coding URL templates. Single
//     link today; structured so adding `links.cancel` etc. later is
//     additive.
type BookingAcceptedResponse struct {
	OrderID uuid.UUID    `json:"order_id"`
	Status  string       `json:"status"`
	Message string       `json:"message"`
	Links   BookingLinks `json:"links"`
}

// BookingLinks holds the polling endpoint for a freshly-accepted
// booking. Separate type so the wire shape `{"links": {"self": ...}}`
// is type-safe and so future additions (cancel, receipt) sit
// alongside without changing the outer response struct.
type BookingLinks struct {
	Self string `json:"self"`
}

// ErrorResponse is the wire shape of every 4xx/5xx response. Centralising
// the shape stops handlers drifting into ad-hoc `gin.H{"error": ...}`
// literals that subtly disagree on field name or status semantics.
type ErrorResponse struct {
	Error string `json:"error"`
}
