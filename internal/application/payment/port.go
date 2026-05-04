package payment

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
)

// ErrInvalidPaymentEvent is returned by Service.ProcessOrder
// when the OrderCreatedEvent payload fails input validation
// (zero/negative OrderID, negative Amount, etc.). Consumers MUST
// treat this as a dead-letter condition: log, publish to DLQ if
// configured, commit the offset, and move on. NEVER a retryable error.
//
// Lives in application (not domain) because it's the application-
// service's contract with its caller (the Kafka consumer). The
// classification "this event will never become valid" is a use-case
// concern, parallel to `domain.IsMalformedOrderInput` for the
// booking-stream worker.
var ErrInvalidPaymentEvent = errors.New("invalid payment event")

// ErrOrderNotAwaitingPayment is returned by CreatePaymentIntent when
// the target order exists but is in a status that doesn't permit a
// payment intent (anything other than `awaiting_payment`). Examples:
// the webhook (D5) already flipped it to Paid; the D6 sweeper
// already flipped it to Expired; or the order is a legacy A4 row
// that never went through Pattern A. The handler maps this to
// HTTP 409 Conflict.
var ErrOrderNotAwaitingPayment = errors.New("order not awaiting payment")

// ErrReservationExpired is returned by CreatePaymentIntent when the
// order's `reserved_until` has elapsed at request time. The reservation
// is logically dead — the D6 sweeper hasn't transitioned it yet, but
// charging would soft-lock inventory beyond the user's promised
// window. Surface as 409 Conflict; the client should re-book to get
// a fresh reservation.
var ErrReservationExpired = errors.New("reservation expired")

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

//go:generate mockgen -source=payment_service.go -destination=../mocks/payment_service_mock.go -package=mocks

// Service defines the application-layer port for the payment
// pipeline. Two responsibilities today:
//
//  1. ProcessOrder — legacy A4 path: Kafka order.created consumer →
//     gateway.Charge → outbox order.failed on failure. Driven by
//     the payment-worker subcommand. D7 narrows or removes this.
//
//  2. CreatePaymentIntent — Pattern A /pay (D4): client-driven,
//     synchronous-from-API-perspective request to register a Stripe-
//     shape PaymentIntent against the gateway. Returns the intent's
//     id + client_secret for the client to use with Stripe Elements.
//     The webhook (D5) is the actual money-movement async callback.
//
// Why both live on one Service interface: they share dependencies
// (gateway, orderRepo) and would just be artificial split. Real
// production services routinely combine "consume async event" and
// "respond to sync request" against the same domain.
//
// Why application (not domain): the input to ProcessOrder is
// `*OrderCreatedEvent`, a wire-format DTO that lives in application.
// A domain-side interface couldn't reference the DTO without
// inverting the dependency rule. Mirrors the same reasoning that put
// OrderQueue in application during PR #37.
//
// `domain.PaymentGateway` (the external-integration port for Stripe /
// MockGateway) stays in domain — that one IS a true domain port,
// no DTOs leak through it.
type Service interface {
	ProcessOrder(ctx context.Context, event *application.OrderCreatedEvent) error

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
