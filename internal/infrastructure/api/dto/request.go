// Package dto holds the HTTP wire-format types — request bodies, query
// strings, and response bodies — that the API layer exchanges with
// clients. Keeping these in their own package decouples the wire
// contract from internal domain shape, so the API can evolve
// independently (versioned routes, additional fields, JSON casing
// changes) without rippling into the domain or persistence layers.
//
// Mappers (in mapper.go) translate between domain values and DTOs.
// Handlers (in api/booking/handler.go) bind requests, invoke services
// with domain types, then map domain results back to DTOs for the
// response.
package dto

import (
	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// BookingRequest is the wire shape of POST /api/v1/book.
//
// D4.1 (KKTIX 票種 alignment): the customer-facing input is a
// `ticket_type_id`, NOT an `event_id`. Pre-D4.1 every event had a
// single global price; post-D4.1 each event carries one or more
// ticket_types (票種) and the ticket_type drives both the price
// snapshot and the inventory key. The event_id is implicit —
// derived from the chosen ticket_type — so the wire shape only
// surfaces the ticket_type_id. Clients discover the available
// ticket_types via `POST /api/v1/events` (response includes
// `ticket_types[]`) or a future `GET /api/v1/events/:id` endpoint.
//
// uuid.UUID's UnmarshalJSON handles parsing. `binding:"required"`
// checks for non-zero value: for uuid.UUID (which is [16]byte) the
// zero value is uuid.Nil, so a missing / empty ticket_type_id is
// correctly rejected at bind time.
//
// `user_id` carries an explicit `min=1`. Without it `required` only
// rejects the int zero-value — a negative id (e.g. `-1`) would slip
// past Gin into the service, where domain.NewReservation finally
// fails with ErrInvalidUserID. Pre-fix the API mapper did not
// translate that sentinel and a malformed client input surfaced as
// 500 Internal Server Error (Codex P2). Shift-left validation here is
// the cheap fix; mapError still carries the matching case as
// defense-in-depth for non-HTTP entry points.
type BookingRequest struct {
	UserID       int       `json:"user_id" binding:"required,min=1"`
	TicketTypeID uuid.UUID `json:"ticket_type_id" binding:"required"`
	Quantity     int       `json:"quantity" binding:"required,min=1,max=10"`
}

// CreateEventRequest is the wire shape of POST /api/v1/events.
//
// D4.1 (KKTIX 票種 alignment): adds `price_cents` + `currency`. The
// event service auto-creates a single default ticket_type carrying
// these as the price snapshot. D8 will replace this with a
// `ticket_types: [{name, price_cents, total, ...}]` array so admins
// can specify multiple 票種 (VIP, 一般, 學生) at create time; for D4.1
// the convenience-default of one ticket_type per event keeps the
// existing API contract one POST.
//
// Validation:
//   - `price_cents` must be ≥ 1 (free / promotional 0-price tickets
//     are out of scope; future `discount_cents` modifier handles
//     that case).
//   - `currency` must be exactly 3 characters at the binding stage.
//     Domain-level NormalizeCurrency lowercases + isValidCurrencyCode
//     does the ASCII-letter shape check; the gateway (Stripe)
//     enforces ISO 4217 membership at charge time.
type CreateEventRequest struct {
	Name         string `json:"name" binding:"required"`
	TotalTickets int    `json:"total_tickets" binding:"required,min=1"`
	PriceCents   int64  `json:"price_cents" binding:"required,min=1"`
	Currency     string `json:"currency" binding:"required,len=3"`
}

// ListBookingsQueryParams binds the query string of GET /api/v1/history.
// Status is a pointer so "absent" and "empty string" are
// distinguishable — the existing code path uses nil to mean "no
// status filter" and a non-nil value to mean "filter to this status".
type ListBookingsQueryParams struct {
	Page   int     `form:"page"`
	Size   int     `form:"size"`
	Status *string `form:"status"`
}

// StatusFilter returns the typed *domain.OrderStatus corresponding to
// the raw string filter, or nil when no filter was provided OR the
// filter value is not a recognised OrderStatus.
//
// Treating an unrecognised value as "no filter" (rather than returning
// an error) keeps the query string forgiving: clients passing
// `?status=expired` don't get a 400 just for not knowing the
// vocabulary. The repo layer would already silently return zero rows
// for an unrecognised filter, but feeding an unvalidated string
// through to SQL is the defense-in-depth gap that S2 closes (Phase 2
// checkpoint Important finding).
//
// If a stricter contract is needed later (e.g., explicit 400 on
// invalid status), a sibling `StatusFilterStrict` method can return
// (status, error). Keeping today's lenient default avoids breaking
// existing clients.
func (q ListBookingsQueryParams) StatusFilter() *domain.OrderStatus {
	if q.Status == nil {
		return nil
	}
	s := domain.OrderStatus(*q.Status)
	if !s.IsValid() {
		return nil
	}
	return &s
}
