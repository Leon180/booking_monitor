package booking

import (
	"context"
	"errors"
	"net/http"

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
		errors.Is(err, domain.ErrOrderNotFound):
		return http.StatusNotFound, "resource not found"

	case errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable, "request canceled"

	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "request timed out"
	}

	// Default: never surface the raw error text. Log it server-side
	// and return a generic message.
	return http.StatusInternalServerError, "internal server error"
}
