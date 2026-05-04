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
//
// D4.1 (KKTIX 票種 alignment): adds `TicketTypeID` + `AmountCents` +
// `Currency` (the price snapshot frozen at book time). Lets a polling
// client display "you reserved 2 × VIP Early Bird at US$20.00 each"
// without separately calling `/pay`. All three use omitempty so
// legacy / pre-D4.1 rows (where the persistence layer coerces SQL
// NULL to zero values) emit a clean response shape without
// suggesting a ticket_type / price exists when it doesn't.
//
// `TicketTypeID` is `*uuid.UUID` (not bare `uuid.UUID`) so the JSON
// encoder can emit nothing for legacy rows — `uuid.UUID` is a fixed-
// size byte array whose zero value (uuid.Nil) marshals as
// `"00000000-..."`, which is operationally indistinguishable from a
// real id. Pointer is the idiomatic Go shape for "this field may
// not exist."
type OrderResponse struct {
	ID            uuid.UUID  `json:"id"`
	EventID       uuid.UUID  `json:"event_id"`
	TicketTypeID  *uuid.UUID `json:"ticket_type_id,omitempty"`
	UserID        int        `json:"user_id"`
	Quantity      int        `json:"quantity"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	ReservedUntil *time.Time `json:"reserved_until,omitempty"`
	AmountCents   int64      `json:"amount_cents,omitempty"`
	Currency      string     `json:"currency,omitempty"`
}

// EventResponse is the wire shape of an event in API responses.
//
// D4.1: adds `ticket_types[]`. Populated by `POST /api/v1/events`
// (creates the event + one default ticket_type) and any future
// `GET /api/v1/events/:id` that loads the event detail. Single-element
// for D4.1 default; multi-element after D8 multi-ticket-type-per-event.
// Empty slice (NOT omitted) when an event has no ticket types yet —
// makes "this event isn't bookable" operationally observable in the
// JSON without a separate flag field.
type EventResponse struct {
	ID               uuid.UUID            `json:"id"`
	Name             string               `json:"name"`
	TotalTickets     int                  `json:"total_tickets"`
	AvailableTickets int                  `json:"available_tickets"`
	Version          int                  `json:"version"`
	TicketTypes      []TicketTypeResponse `json:"ticket_types"`
}

// TicketTypeResponse is the wire shape of a ticket_type (KKTIX 票種)
// nested under an event. Mirrors `domain.TicketType` field layout but
// exposes only the fields the client cares about — `version` is
// internal optimistic-concurrency state, not surfaced.
//
// Optional fields (`sale_starts_at`, `sale_ends_at`, `per_user_limit`,
// `area_label`) use omitempty so a default ticket_type with no sale
// window / no limit / no area emits a clean `{id, name, price_cents,
// currency, total_tickets, available_tickets}` object — clients can
// reliably check `if "sale_starts_at" in tt` to know whether the
// operator set the field, distinct from "set but expired."
type TicketTypeResponse struct {
	ID               uuid.UUID  `json:"id"`
	EventID          uuid.UUID  `json:"event_id"`
	Name             string     `json:"name"`
	PriceCents       int64      `json:"price_cents"`
	Currency         string     `json:"currency"`
	TotalTickets     int        `json:"total_tickets"`
	AvailableTickets int        `json:"available_tickets"`
	SaleStartsAt     *time.Time `json:"sale_starts_at,omitempty"`
	SaleEndsAt       *time.Time `json:"sale_ends_at,omitempty"`
	PerUserLimit     *int       `json:"per_user_limit,omitempty"`
	AreaLabel        string     `json:"area_label,omitempty"`
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

// PaymentIntentResponse is the wire shape returned by
// POST /api/v1/orders/:id/pay (D4). Pairs with the Stripe Elements
// pattern: client gets `client_secret` and uses it to confirm payment
// client-side; the actual money movement lands via the D5 webhook.
//
// Why these fields:
//   - `OrderID` echoes the path param so logs / tracing have a single
//     correlation handle without parsing the URL.
//   - `PaymentIntentID` is persisted to the order row and used by the
//     D5 webhook handler to look up the order. Stripe shape:
//     "pi_3xxx...".
//   - `ClientSecret` is the sensitive token Stripe Elements consumes
//     to confirm payment. NOT persisted on our side — Stripe's
//     recommended posture is "client gets it once via API, server
//     re-fetches via Retrieve if needed". Our mock returns the same
//     secret for repeat retrieves to model that.
//   - `AmountCents` + `Currency` echo what the gateway agreed to
//     charge. Useful for the client to render "you'll be charged
//     $20.00 USD" without recomputing from order quantity.
//
// Idempotency: repeat POSTs to /pay return the same intent (gateway
// is idempotent on orderID). Clients don't need to cache or retry-
// guard themselves.
type PaymentIntentResponse struct {
	OrderID         uuid.UUID `json:"order_id"`
	PaymentIntentID string    `json:"payment_intent_id"`
	ClientSecret    string    `json:"client_secret"`
	AmountCents     int64     `json:"amount_cents"`
	Currency        string    `json:"currency"`
}
