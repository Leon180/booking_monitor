package domain

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

//go:generate mockgen -source=payment.go -destination=../mocks/payment_mock.go -package=mocks

// Payment-adapter sentinels (D4.2). Real gateway adapters (e.g.,
// `internal/infrastructure/payment/stripe_gateway.go`) wrap underlying
// SDK errors with `errors.Join(serr, ErrPayment*)` so callers in the
// `/pay` handler and the reconciler can switch on the failure class
// via `errors.Is(err, ErrPayment*)` without leaking adapter-specific
// types up the call stack.
//
// Mock gateway (`MockGateway`) doesn't currently emit any of these —
// "always succeed" mock semantics never fail. The sentinels exist to
// classify real-gateway failures.
var (
	// ErrPaymentDeclined — gateway refused the charge for a reason
	// the buyer can address (card declined, expired card, insufficient
	// funds, fraud rule). The /pay handler maps this to 422
	// Unprocessable Entity with a generic non-leaky message; client UX
	// surfaces "card was declined; try another card".
	ErrPaymentDeclined = errors.New("payment declined")

	// ErrPaymentTransient — gateway-side transient failure: 429 rate-
	// limit, 5xx, network error, gateway timeout. Caller may retry with
	// backoff; the gateway-side idempotency contract (Idempotency-Key
	// keyed on order_id) makes retries safe. Recon classifies this as
	// "skip + retry next sweep". Production alert: rate > N/min.
	ErrPaymentTransient = errors.New("payment transient")

	// ErrPaymentMisconfigured — gateway rejected on auth / permission
	// (401, 403). This is a CONFIG error: bad API key, key revoked,
	// restricted-scope key missing required permission. Always
	// page-worthy; never recoverable by retry.
	ErrPaymentMisconfigured = errors.New("payment misconfigured")

	// ErrPaymentInvalid — gateway rejected on programmer error (400,
	// idempotency-key collision with different params, bad currency
	// code). Caller should NOT retry — same input produces same error.
	// /pay handler maps to 422 with a non-leaky message; operator
	// investigates root cause.
	ErrPaymentInvalid = errors.New("payment invalid request")
)

// ChargeStatus is the gateway-side outcome of a charge, observed via
// PaymentStatusReader.GetStatus. Plain string constants (matching the
// OrderStatus convention in this codebase) so values survive
// marshaling without a Stringer, are human-readable in logs, and can
// be compared against gateway wire vocabulary directly.
//
// The zero value (`ChargeStatus("")`) is INTENTIONALLY equivalent to
// the explicit `ChargeStatusUnknown` constant so an uninitialized
// ChargeStatus from a buggy adapter is never silently treated as
// "charged" — callers that switch on the value with no default
// will fall into the Unknown branch (loud-by-default).
type ChargeStatus string

const (
	// ChargeStatusUnknown — the gateway returned a verdict the adapter
	// could not classify, or the adapter is in a state where it has
	// no opinion yet. Reconciler treats Unknown as "skip this order
	// for now, retry next sweep cycle"; emit a metric so a sustained
	// Unknown rate is alertable.
	//
	// Distinct from "GetStatus call itself failed" — that's a non-nil
	// error (network, gateway 5xx). Unknown means "the call succeeded
	// but the answer doesn't fit any of the other three buckets".
	ChargeStatusUnknown ChargeStatus = ""

	// ChargeStatusCharged — the gateway confirms the customer was
	// charged. Reconciler transitions the order Charging → Confirmed.
	ChargeStatusCharged ChargeStatus = "charged"

	// ChargeStatusDeclined — the gateway refused the charge (declined
	// card, fraud rule, etc.). Reconciler transitions Charging → Failed,
	// triggering the saga compensation path identical to a normal
	// payment-failure flow.
	ChargeStatusDeclined ChargeStatus = "declined"

	// ChargeStatusNotFound — the gateway has no record of an attempt
	// for this orderID. Means our payment service crashed AFTER
	// MarkCharging committed but BEFORE Charge() was called. Recon
	// treats NotFound as "safe to drop back to Pending" — but in this
	// PR's scope we keep things simple: NotFound also routes to
	// Charging → Failed with a "no gateway record" reason. Worker can
	// re-invoke via Kafka redelivery if Pending is restored later.
	ChargeStatusNotFound ChargeStatus = "not_found"
)

// PaymentStatusReader is the read side. The reconciler subcommand is
// the sole consumer; the payment-worker does NOT have this port in
// scope (it doesn't need to query — it owns the Charge call).
//
// D4.2 changed the parameter from `orderID uuid.UUID` to
// `paymentIntentID string` to match the real Stripe API shape — Stripe
// has no "get intent by metadata.order_id" cheap call, only "get
// intent by ID". The intent ID lives on `orders.payment_intent_id`;
// the reconciler reads it via `OrderRepository.FindStuckCharging`'s
// extended `domain.StuckCharging.PaymentIntentID` field and threads
// it explicitly into this method. See the null-guard in
// `Reconciler.resolve` for the legacy "intent ID NULL" case (caused
// by the documented `SetPaymentIntentID` race in
// `internal/application/payment/service.go`).
//
// GetStatus returns one of four ChargeStatus values plus a possibly-
// non-nil error. The (status, err) pair is NOT mutually exclusive:
//
//   - (Charged | Declined | NotFound, nil)  — clean verdict, no infra failure
//   - (Unknown,  nil)                       — gateway answered but the
//                                             answer is unclassifiable.
//                                             Reconciler retries next sweep.
//   - (_, non-nil err)                      — infrastructure failure
//                                             (network, gateway 5xx, ctx
//                                             timeout, or any of the
//                                             ErrPayment* sentinels above).
//                                             Reconciler treats as
//                                             transient: emit error
//                                             metric, retry next sweep.
//
// Note: returning `(Charged, err)` is permitted — the gateway might
// answer "yes charged" but the adapter's transport layer fails on
// the response read. Caller MUST inspect err first.
type PaymentStatusReader interface {
	GetStatus(ctx context.Context, paymentIntentID string) (ChargeStatus, error)
}

// PaymentIntent is the gateway-issued reservation that lets a client
// (typically Stripe Elements in the browser) actually move money. It
// pairs with the Pattern A flow: BookTicket reserves inventory →
// /pay creates a PaymentIntent → client uses ClientSecret to confirm
// payment → webhook flips the order to Paid (D5).
//
// Why a struct rather than `(intentID, clientSecret string)`:
// extensible without breaking adapters (adding `Status` for
// `requires_action` / `succeeded` / `requires_payment_method` is a
// future Stripe nuance), and keeps gateway implementations honest
// about returning amount/currency that match what was requested.
type PaymentIntent struct {
	// ID is the gateway-assigned identifier (Stripe shape: "pi_3xxx...").
	// Persisted to `orders.payment_intent_id` so the D5 webhook handler
	// can match incoming events back to the order. The gateway MUST
	// return the SAME ID for repeat calls with the same orderID — this
	// is the gateway-side idempotency contract that makes /pay
	// retry-safe without per-request idempotency keys at our layer.
	ID string

	// ClientSecret is the sensitive token Stripe Elements uses to
	// confirm the payment client-side. Returned in API responses but
	// NOT persisted to DB — Stripe's recommended posture is to fetch
	// it via Retrieve on subsequent reads rather than store it.
	// (Real-world: storing client_secret means it leaks in DB dumps;
	// retrieving from gateway gives a single security perimeter.)
	ClientSecret string

	// AmountCents is the smallest-unit amount the intent will charge.
	// Stripe convention: USD $10.00 = 1000 cents. int64 because
	// floats in money-handling code is the OWASP-listed anti-pattern.
	AmountCents int64

	// Currency is the ISO 4217 three-letter code ("USD", "TWD", etc.)
	// in lowercase per Stripe convention.
	Currency string

	// Metadata is the gateway-side attribute bag — Stripe's convention
	// for threading caller-controlled context through to the eventual
	// webhook delivery (`{"order_id": "<uuid>"}`). The D5 webhook
	// handler reads `metadata["order_id"]` as the PRIMARY lookup key
	// when resolving an inbound `payment_intent.*` event back to its
	// order, falling back to `payment_intent_id` only when metadata is
	// missing or malformed (legacy intent created before this field
	// was added, or a foreign / cross-env event).
	//
	// Why metadata instead of just relying on payment_intent_id:
	// `service.CreatePaymentIntent` has a documented race
	// (internal/application/payment/service.go:331) where the gateway
	// successfully registers the intent but the `SetPaymentIntentID`
	// UPDATE fails — leaving an "orphan" intent on the gateway side
	// with no payment_intent_id row on our side. The customer can
	// still complete payment via Stripe Elements; the resulting
	// webhook is the ONLY signal we get that the money moved.
	// metadata.order_id rescues that path.
	Metadata map[string]string
}

// PaymentIntentCreator is the Pattern A write port — creates a
// PaymentIntent at the gateway. The application service for /pay (D4)
// is the sole consumer; the reconciler and saga do NOT receive this
// port.
//
// IDEMPOTENCY CONTRACT — same rule real Stripe / Adyen adapters
// enforce via their `Idempotency-Key` header: implementations MUST
// treat orderID as the gateway-side idempotency key. Repeat
// CreatePaymentIntent calls with the same orderID MUST return the
// SAME `PaymentIntent` value (same ID, same ClientSecret, same
// AmountCents, same Currency) — they MUST NOT register a second
// intent with the gateway. Real Stripe adapters pass orderID as the
// `Idempotency-Key` header on `POST /v1/payment_intents`.
//
// This contract makes /pay safe to retry without our application
// layer maintaining its own idempotency cache: the gateway IS the
// cache. The application service can call CreatePaymentIntent every
// time and rely on the gateway returning the cached intent.
type PaymentIntentCreator interface {
	CreatePaymentIntent(ctx context.Context, orderID uuid.UUID, amountCents int64, currency string, metadata map[string]string) (PaymentIntent, error)
}

// PaymentGateway is the combined port retained for adapters that
// implement both halves (status read + intent creation). New consumers
// should depend on the narrowest port that fits their needs
// (`PaymentStatusReader` for recon's gateway probe, `PaymentIntentCreator`
// for D4's /pay handler) and the fx providers in `cmd/booking-cli/server.go`
// advertise the narrow halves directly. Kept here (not deleted) because
// MockGateway in the test infrastructure is the single adapter today
// and naturally implements both — splitting the adapter into separate
// types just to satisfy a "narrow port at the adapter" rule would add
// ceremony without payoff.
//
// Real Stripe / Adyen / Braintree adapters in production would
// likewise implement multiple sides on a single struct.
//
// D7 (2026-05-08) deleted `PaymentCharger` (and its `Charge` method) —
// the legacy A4 auto-charge path is gone, so no consumer requests
// `Charge` anymore. Pattern A drives money movement through
// `PaymentIntentCreator.CreatePaymentIntent` (D4) + the provider
// webhook (D5), neither of which calls `Charge`.
type PaymentGateway interface {
	PaymentStatusReader
	PaymentIntentCreator
}
