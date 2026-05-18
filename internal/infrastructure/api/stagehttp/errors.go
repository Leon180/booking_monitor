package stagehttp

import (
	"errors"
	"net/http"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/domain"
)

// MapBookingError translates booking.Service errors (and the
// stagehttp Compensator's ErrCompensateNotEligible) into HTTP
// status + public error message. Mirrors the existing api/booking
// (Stage 4) error mapping so the comparison harness sees identical
// 400/404/409/500 codes across all four stages — the apples-to-
// apples contract guarantee for the D9 k6 script.
//
// Sentinel coverage:
//
//   - domain.ErrTicketTypeNotFound        → 404 (booking)
//   - domain.ErrOrderNotFound             → 404 (get_order)
//   - domain.ErrSoldOut                   → 409 (booking)
//   - domain.ErrUserAlreadyBought         → 409 (booking; from
//                                            uq_orders_user_event,
//                                            mapped via 23505 in
//                                            sync.Service)
//   - domain.ErrInvalidUserID            \
//   - domain.ErrInvalidQuantity          | → 400 (booking validation)
//   - domain.ErrInvalidOrderTicketTypeID /
//   - ErrCompensateNotEligible           → 409 (confirm-failed only)
//
// Anything else is wrapped to 500 + opaque "internal error" — leak
// guard for unexpected SQL / driver-state errors.
func MapBookingError(err error) (int, string) {
	switch {
	// Stage 5 Kafka publish failure — matched FIRST so the
	// errors.Is chain walk doesn't dispatch to a transitively
	// wrapped domain sentinel (e.g. a future refactor that lets
	// publishErr wrap ErrSoldOut would otherwise return 409
	// "sold out" for a Kafka outage). Map to 503 Service
	// Unavailable per RFC 7231 §6.6.4 — the request was valid,
	// the durability gate is temporarily down.
	case errors.Is(err, booking.ErrStage5PublishFailed):
		return http.StatusServiceUnavailable, "service temporarily unavailable; retry"
	case errors.Is(err, domain.ErrTicketTypeNotFound):
		return http.StatusNotFound, "ticket_type not found"
	case errors.Is(err, domain.ErrOrderNotFound):
		return http.StatusNotFound, "order not found"
	case errors.Is(err, domain.ErrSoldOut):
		return http.StatusConflict, "sold out"
	case errors.Is(err, domain.ErrUserAlreadyBought):
		return http.StatusConflict, "user has already booked this event"
	case errors.Is(err, ErrCompensateNotEligible):
		return http.StatusConflict, "order not in awaiting_payment state"
	case errors.Is(err, domain.ErrInvalidUserID),
		errors.Is(err, domain.ErrInvalidQuantity),
		errors.Is(err, domain.ErrInvalidOrderTicketTypeID):
		return http.StatusBadRequest, "invalid request parameters"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}
