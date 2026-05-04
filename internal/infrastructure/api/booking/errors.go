package booking

import (
	"context"
	"errors"
	"net/http"

	paymentapp "booking_monitor/internal/application/payment"
	"booking_monitor/internal/domain"
)

// mapError translates an internal error into a safe HTTP status code and
// a public, user-facing message. It never leaks driver-level, SQL, or
// stack-trace details to clients.
//
// Callers should log the original `err` via their injected logger (with
// request_id / correlation_id) BEFORE calling this helper so the full
// context remains server-side only.
//
// The second return value is the JSON-safe public message that may be
// rendered in a response body.
//
// Lives in the booking package because every consumer is a booking
// handler. Operational endpoints (ops/health.go) carry their own
// per-dependency error shape — there's no cross-package translation
// to share.
func mapError(err error) (status int, publicMsg string) {
	if err == nil {
		return http.StatusOK, ""
	}

	switch {
	case errors.Is(err, domain.ErrSoldOut):
		return http.StatusConflict, "sold out"

	case errors.Is(err, domain.ErrUserAlreadyBought):
		return http.StatusConflict, "user already bought ticket"

	case errors.Is(err, domain.ErrEventNotFound),
		errors.Is(err, domain.ErrOrderNotFound),
		errors.Is(err, domain.ErrTicketTypeNotFound):
		return http.StatusNotFound, "resource not found"

	case errors.Is(err, domain.ErrInvalidOrderID):
		return http.StatusBadRequest, "invalid order id"

	// D4.1 — KKTIX-aligned event/ticket-type creation invariants.
	// All originate at the domain factories (NewEvent / NewTicketType)
	// and escape unwrapped from event.Service.CreateEvent. Without
	// these cases the default branch fires and a malformed request body
	// silently surfaces as 500, paging on-call for what is a 4xx.
	case errors.Is(err, domain.ErrInvalidEventName),
		errors.Is(err, domain.ErrInvalidTotalTickets),
		errors.Is(err, domain.ErrInvalidTicketTypeID),
		errors.Is(err, domain.ErrInvalidTicketTypeEventID),
		errors.Is(err, domain.ErrInvalidTicketTypeName),
		errors.Is(err, domain.ErrInvalidTicketTypePrice),
		errors.Is(err, domain.ErrInvalidTicketTypeCurrency),
		errors.Is(err, domain.ErrInvalidTicketTypeTotal),
		errors.Is(err, domain.ErrInvalidTicketTypeAvailable),
		errors.Is(err, domain.ErrInvalidTicketTypeSaleWindow),
		errors.Is(err, domain.ErrInvalidTicketTypePerUser):
		return http.StatusBadRequest, "invalid event parameters"

	// 23505 unique-constraint violation on (event_id, name) when an
	// admin tries to create a duplicate-named ticket type. Surfaced as
	// 409 Conflict — the caller can resolve by picking a different name.
	case errors.Is(err, domain.ErrTicketTypeNameTaken):
		return http.StatusConflict, "ticket type name already exists for this event"

	// D4 Pattern A /pay errors. Both surface as 409 Conflict because
	// they describe a state mismatch the client could resolve by
	// re-reading the order or re-booking; not a 4xx-malformed-input
	// problem.
	case errors.Is(err, paymentapp.ErrOrderNotAwaitingPayment):
		return http.StatusConflict, "order is not awaiting payment"

	case errors.Is(err, paymentapp.ErrReservationExpired):
		return http.StatusConflict, "reservation expired"

	case errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable, "request canceled"

	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request timed out"
	}

	// Default: never surface the raw error text. Log it server-side
	// and return a generic message.
	return http.StatusInternalServerError, "internal server error"
}

// isExpectedPayError reports whether err is one of the
// /pay-specific business outcomes the handler should log at Warn
// (not Error). Keeps log dashboards clean: 404 / 409 paths are
// expected client-side state transitions, not internal failures.
//
// Pinned in errors.go (not handler.go) to live alongside the
// authoritative mapError so a future sentinel addition updates both
// in one place.
func isExpectedPayError(err error) bool {
	return errors.Is(err, domain.ErrOrderNotFound) ||
		errors.Is(err, domain.ErrInvalidOrderID) ||
		errors.Is(err, paymentapp.ErrOrderNotAwaitingPayment) ||
		errors.Is(err, paymentapp.ErrReservationExpired)
}
