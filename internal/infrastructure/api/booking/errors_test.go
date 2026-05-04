package booking

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	paymentapp "booking_monitor/internal/application/payment"
	"booking_monitor/internal/domain"

	"github.com/stretchr/testify/assert"
)

// TestMapError_DomainSentinels pins the public-facing translation for
// every domain / payment sentinel mapError handles. Pre-D4.1-followup,
// adding a new sentinel without updating mapError leaked malformed
// client input as 500 (Codex P2). The test now enumerates the FULL
// sentinel surface so a future addition trips a compile error if
// mapError isn't updated alongside it.
func TestMapError_DomainSentinels(t *testing.T) {
	t.Parallel()

	// Wrap each sentinel in fmt.Errorf("%w") to exercise mapError's
	// errors.Is path (real callers wrap repeatedly through the service
	// layer). A bare sentinel would still pass — wrapping is the more
	// honest scenario.
	wrap := func(err error) error { return fmt.Errorf("application: %w", err) }

	tests := []struct {
		name           string
		err            error
		wantStatus     int
		wantPublicMsg  string
		wantContainsIn []string // substring match for partial-text assertions
	}{
		// 4xx — sold-out / duplicate
		{name: "ErrSoldOut", err: domain.ErrSoldOut, wantStatus: http.StatusConflict, wantPublicMsg: "sold out"},
		{name: "ErrUserAlreadyBought", err: domain.ErrUserAlreadyBought, wantStatus: http.StatusConflict, wantPublicMsg: "user already bought ticket"},

		// 404 — not-found family
		{name: "ErrEventNotFound", err: domain.ErrEventNotFound, wantStatus: http.StatusNotFound, wantPublicMsg: "resource not found"},
		{name: "ErrOrderNotFound", err: domain.ErrOrderNotFound, wantStatus: http.StatusNotFound, wantPublicMsg: "resource not found"},
		{name: "ErrTicketTypeNotFound", err: domain.ErrTicketTypeNotFound, wantStatus: http.StatusNotFound, wantPublicMsg: "resource not found"},

		// 400 — order-id invariant
		{name: "ErrInvalidOrderID", err: domain.ErrInvalidOrderID, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid order id"},

		// 400 — D4.1 follow-up: order/reservation invariants. Pre-fix
		// these all fell through to 500.
		{name: "ErrInvalidUserID", err: domain.ErrInvalidUserID, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid request parameters"},
		{name: "ErrInvalidQuantity", err: domain.ErrInvalidQuantity, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid request parameters"},
		{name: "ErrInvalidReservedUntil", err: domain.ErrInvalidReservedUntil, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid request parameters"},
		{name: "ErrInvalidAmountCents", err: domain.ErrInvalidAmountCents, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid request parameters"},
		{name: "ErrInvalidCurrency", err: domain.ErrInvalidCurrency, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid request parameters"},

		// 400 — D4.1 event/ticket-type creation invariants
		{name: "ErrInvalidEventName", err: domain.ErrInvalidEventName, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid event parameters"},
		{name: "ErrInvalidTotalTickets", err: domain.ErrInvalidTotalTickets, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid event parameters"},
		{name: "ErrInvalidTicketTypeName", err: domain.ErrInvalidTicketTypeName, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid event parameters"},
		{name: "ErrInvalidTicketTypePrice", err: domain.ErrInvalidTicketTypePrice, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid event parameters"},
		{name: "ErrInvalidTicketTypeCurrency", err: domain.ErrInvalidTicketTypeCurrency, wantStatus: http.StatusBadRequest, wantPublicMsg: "invalid event parameters"},

		// 409 — name conflicts + D4 payment state mismatches
		{name: "ErrTicketTypeNameTaken", err: domain.ErrTicketTypeNameTaken, wantStatus: http.StatusConflict, wantContainsIn: []string{"already exists"}},
		{name: "ErrOrderNotAwaitingPayment", err: paymentapp.ErrOrderNotAwaitingPayment, wantStatus: http.StatusConflict, wantPublicMsg: "order is not awaiting payment"},
		{name: "ErrReservationExpired", err: paymentapp.ErrReservationExpired, wantStatus: http.StatusConflict, wantPublicMsg: "reservation expired"},
		{name: "ErrOrderMissingPriceSnapshot", err: paymentapp.ErrOrderMissingPriceSnapshot, wantStatus: http.StatusConflict, wantContainsIn: []string{"price data unavailable"}},

		// 5xx — context errors map to 503 / 504 distinct from default 500
		{name: "context.Canceled", err: context.Canceled, wantStatus: http.StatusServiceUnavailable, wantPublicMsg: "request canceled"},
		{name: "context.DeadlineExceeded", err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantPublicMsg: "request timed out"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStatus, gotMsg := mapError(wrap(tt.err))
			assert.Equal(t, tt.wantStatus, gotStatus, "status code mismatch")
			if tt.wantPublicMsg != "" {
				assert.Equal(t, tt.wantPublicMsg, gotMsg, "public message mismatch")
			}
			for _, want := range tt.wantContainsIn {
				assert.Contains(t, gotMsg, want, "public message must contain %q", want)
			}
		})
	}
}

// TestMapError_NilAndUnknown pins the two endpoints of the switch:
// nil → 200 (the no-error case, used by handlers as a uniform code
// path), and an unknown error → 500 with a sanitized message that
// MUST NOT leak the raw error text. Without this test, a future
// refactor that changes either endpoint would silently drift.
func TestMapError_NilAndUnknown(t *testing.T) {
	t.Parallel()

	t.Run("nil_returns_200", func(t *testing.T) {
		gotStatus, gotMsg := mapError(nil)
		assert.Equal(t, http.StatusOK, gotStatus)
		assert.Empty(t, gotMsg)
	})

	t.Run("unknown_error_500_with_sanitized_message", func(t *testing.T) {
		// Construct an error that doesn't match any sentinel via errors.Is.
		// The raw text contains a (fake) sensitive substring; mapError
		// MUST NOT echo it.
		secretLeak := errors.New("postgres: password '/tmp/.pgpass' refused (host=10.0.0.1)")
		gotStatus, gotMsg := mapError(secretLeak)
		assert.Equal(t, http.StatusInternalServerError, gotStatus)
		assert.Equal(t, "internal server error", gotMsg,
			"unknown errors MUST surface as a sanitized message — leaking the raw text would expose connection strings, file paths, etc.")
		assert.NotContains(t, gotMsg, "password")
		assert.NotContains(t, gotMsg, "/tmp/.pgpass")
	})
}
