package payment

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// ErrOrderNotAwaitingPayment is returned by CreatePaymentIntent when
// the target order exists but is in a status that doesn't permit a
// payment intent (anything other than `awaiting_payment`). Examples:
// the webhook (D5) already flipped it to Paid; the D6 sweeper
// already flipped it to Expired; or the order is a legacy A4 row
// that never went through Pattern A. The handler maps this to
// HTTP 409 Conflict.
var ErrOrderNotAwaitingPayment = errors.New("order not awaiting payment")

// ErrReservationExpired is a backwards-compat alias for
// `domain.ErrReservationExpired` — the single canonical sentinel
// returned across the service-level pre-check
// (`CreatePaymentIntent`'s `!order.ReservedUntil().After(now)` guard)
// and the SQL-level predicates in D5 (`SetPaymentIntentID` /
// `MarkPaid`). New code in this package SHOULD prefer the
// `domain.ErrReservationExpired` form; the alias exists so existing
// imports and callers continue to compile without qualification
// changes. `errors.Is` works with either form because they are the
// same pointer.
//
// DO NOT REASSIGN this var to a fresh sentinel — that would silently
// split "reservation expired" into two non-equivalent values across
// packages. If you find yourself wanting a new sentinel, define it in
// `domain` and add a separate alias here.
//
// Surface as 409 Conflict; the client should re-book to get a fresh
// reservation.
var ErrReservationExpired = domain.ErrReservationExpired

// ErrOrderMissingPriceSnapshot is returned by CreatePaymentIntent when
// the order is in `awaiting_payment` AND the reservation is still
// valid, but the (amount_cents, currency) price snapshot is missing
// (`order.HasPriceSnapshot()` false). Two paths produce this state:
//
//   1. Legacy / pre-D4.1 order rows where the persistence layer
//      coerced SQL NULL to (0, ""). New D4.1 orders always have the
//      snapshot set by `domain.NewReservation`; legacy rows in
//      `awaiting_payment` should not exist by this PR's cutover, but
//      this guard catches the case if migration leaves them behind.
//   2. A future state-machine bug where a D4.1 order somehow loses
//      its snapshot. This is the load-bearing case — it's a real
//      data-integrity defect that must page on-call, NOT a routine
//      client-side state-mismatch like ErrOrderNotAwaitingPayment.
//
// Mapped to HTTP 409 Conflict (same status as the other two /pay
// rejects) but with a distinct public message + DELIBERATELY excluded
// from `isExpectedPayError` so the handler logs at Error (not Warn).
// Operators investigating "why is /pay returning 409" can then
// distinguish data-integrity bugs from routine state transitions.
var ErrOrderMissingPriceSnapshot = errors.New("order missing price snapshot")

//go:generate mockgen -source=port.go -destination=../../mocks/payment_service_mock.go -package=mocks

// Service defines the application-layer port for the payment
// pipeline. After D7 the legacy A4 auto-charge path
// (`ProcessOrder` + payment-worker subcommand consuming
// `order.created`) is gone — Pattern A drives money movement entirely
// through `/api/v1/orders/:id/pay` (D4) plus the provider webhook
// (D5). Saga compensation (`order.failed`) now has only two
// production emitters: D5's webhook on `payment_failed` and D6's
// expiry sweeper. Recon's `failOrder` is a third (rare) emitter for
// stuck-charging force-fails, tagged via `Reason="recon: ..."`.
//
// `domain.PaymentGateway` (the external-integration port for Stripe /
// MockGateway) stays in domain — that one IS a true domain port,
// no DTOs leak through it.
type Service interface {
	// CreatePaymentIntent is the Pattern A /pay (D4) entry point:
	// validates the order is in awaiting_payment + not expired, calls
	// the gateway's CreatePaymentIntent (idempotent on orderID per
	// the PaymentIntentCreator contract), persists the intent_id on
	// the order row, and returns the PaymentIntent for the API
	// response.
	//
	// Returns (zero-value PaymentIntent, error) on:
	//   - domain.ErrOrderNotFound      no order with that id
	//   - ErrOrderNotAwaitingPayment    order is in a different status
	//                                   (already Paid / Expired / etc.)
	//   - ErrReservationExpired         reserved_until has elapsed
	//   - other errors                  gateway / DB transient failure
	//
	// Idempotent at the application boundary: repeat calls return the
	// same PaymentIntent (gateway-side caching makes this safe).
	CreatePaymentIntent(ctx context.Context, orderID uuid.UUID) (domain.PaymentIntent, error)
}
