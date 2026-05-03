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
//
// D3 (Pattern A): adds `ReservedUntil` for AwaitingPayment / Expired
// orders. Pointer + omitempty so legacy A4 rows (zero-value
// reservedUntil) marshal without the field rather than as RFC3339
// "0001-01-01T00:00:00Z" — clients can reliably check
// `if "reserved_until" in response` to branch Pattern A from legacy.
type OrderResponse struct {
	ID            uuid.UUID  `json:"id"`
	EventID       uuid.UUID  `json:"event_id"`
	UserID        int        `json:"user_id"`
	Quantity      int        `json:"quantity"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	ReservedUntil *time.Time `json:"reserved_until,omitempty"`
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

// BookingStatus is a typed enum for the `status` field on
// BookingAcceptedResponse and any future polling responses. Typed
// rather than raw `string` so a typo at the call site (`"Reserved"`
// with capital R, `"queued"`, etc.) fails at compile time. This
// matters because the wire field is part of the public API contract;
// silent drift between the producer and the documented set is
// exactly the bug the type system can prevent.
//
// D3 (Pattern A): the status returned by POST /api/v1/book changed
// from `processing` to `reserved`. Same 202 Accepted code, same
// async-pipeline semantics, but the verb now matches what actually
// happens — Redis inventory has been reserved (not just "queued for
// processing"), and the client is expected to call /pay (D4) to
// complete the purchase before the reservation TTL elapses.
// `processing` is kept as a deprecated constant so mid-flight clients
// observing an old in-process worker don't break.
//
// `OrderResponse.Status` (returned by `GET /orders/:id`) keeps a
// raw `string` to mirror `domain.OrderStatus`, which has its own
// terminal-state vocabulary (`pending`, `charging`, `confirmed`,
// `failed`, `compensated`, `awaiting_payment`, `paid`, `expired`,
// `payment_failed`). The two enums describe different things and live
// at different layers — don't conflate them.
type BookingStatus string

const (
	// BookingStatusReserved is the canonical D3 / Pattern A response
	// status. The order is in `awaiting_payment` on the domain side;
	// the wire status is the user-facing verb ("your seats are
	// reserved, you have N minutes to pay"). Pair with
	// BookingAcceptedResponse.Links.Pay to direct the client to the
	// next step.
	BookingStatusReserved BookingStatus = "reserved"

	// BookingStatusProcessing is the legacy A4 response status. Kept
	// for backwards compatibility — clients pinned to the old vocabulary
	// will continue to receive a 202 + this string. Newly-deployed
	// servers always emit BookingStatusReserved.
	BookingStatusProcessing BookingStatus = "processing"
)

// BookingAcceptedResponse is the wire shape returned by POST /api/v1/book
// on success. The 202 semantics are honest about the async pipeline:
// the Redis-side inventory deduct succeeded (the "gate"), the
// reservation has been queued for DB persistence, and the rest of
// the Pattern A flow (client calls /pay → webhook flips to Paid OR
// the D6 sweeper flips to Expired) is up to the client. Clients use
// the returned `OrderID` against `GET /api/v1/orders/:id` to track
// status, and `Links.Pay` to initiate payment.
//
// Why this shape:
//   - `OrderID` is the canonical correlation handle. Echoed in the
//     server logs (correlation_id), tracing span (order_id attr),
//     and DB orders.id. A customer reporting "my booking is missing"
//     hands over the order_id and operators have a single string to
//     pivot from.
//   - `Status` is the application-layer state at the moment of
//     response — `reserved` means "Redis accepted, worker is
//     persisting the row as AwaitingPayment". Kept distinct from
//     HTTP status so a future synchronous-confirmation flow could
//     return 200 + status:"confirmed".
//   - `ReservedUntil` is the absolute time the reservation expires.
//     Emitted as RFC3339 UTC ("2026-05-03T15:30:00Z") so client-side
//     countdown logic doesn't need to assume a clock-sync model.
//   - `ExpiresInSeconds` is a convenience derived from
//     `ReservedUntil - now()` at response-build time. Some clients
//     (mobile, low-power IoT) prefer a relative duration to a
//     wallclock; both are emitted so neither side needs to compute
//     the other.
//   - `Links.Self` follows the loose HAL/JSON:API convention so
//     clients can navigate without hard-coding URL templates.
//     `Links.Pay` is added in D3 to surface the D4 endpoint (POST
//     /api/v1/orders/:id/pay) — currently a placeholder route; the
//     handler returns 501 / 404 until D4 wires it.
type BookingAcceptedResponse struct {
	OrderID          uuid.UUID     `json:"order_id"`
	Status           BookingStatus `json:"status"`
	Message          string        `json:"message"`
	ReservedUntil    time.Time     `json:"reserved_until"`
	ExpiresInSeconds int           `json:"expires_in_seconds"`
	Links            BookingLinks  `json:"links"`
}

// BookingLinks holds the navigation endpoints for a freshly-accepted
// booking. Separate type so the wire shape `{"links": {...}}` is
// type-safe and so future additions (cancel, receipt) sit alongside
// without changing the outer response struct.
type BookingLinks struct {
	Self string `json:"self"`
	// Pay is the URL the client POSTs to in order to initiate payment
	// against the reserved order. Pattern A flow: client receives this
	// link in the 202 response, posts the (Stripe Elements) card token
	// against it. D4 wires the actual handler; until then this URL
	// resolves to a 404. Empty string for legacy A4 responses where
	// the server auto-charged and no client-driven payment step exists.
	Pay string `json:"pay,omitempty"`
}

// ErrorResponse is the wire shape of every 4xx/5xx response. Centralising
// the shape stops handlers drifting into ad-hoc `gin.H{"error": ...}`
// literals that subtly disagree on field name or status semantics.
type ErrorResponse struct {
	Error string `json:"error"`
}
