package domain

import (
	"context"

	"github.com/google/uuid"
)

//go:generate mockgen -source=payment.go -destination=../mocks/payment_mock.go -package=mocks

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

// PaymentCharger is the write side of payment processing. The
// payment-worker service (KafkaConsumer for order.created) is the
// sole consumer; the reconciler does NOT receive this port to
// eliminate "accidental Charge call from recon code" as a class of
// bug. Mirrors the io.Reader / io.Writer separation in stdlib.
//
// IDEMPOTENCY CONTRACT — load-bearing, the payment service relies on
// it:
//
// Implementations MUST treat orderID as an idempotency key. Repeat
// Charge calls with the same orderID MUST return the result of the
// FIRST call — they MUST NOT initiate a second charge against the
// payment provider. Real adapters pass orderID as the provider's
// `Idempotency-Key` header (Stripe), `idempotency_key` field
// (Square), or equivalent.
//
// Why this contract is required even after A4's intent log:
//
//	A4 records intent (status=Charging) BEFORE calling Charge, so
//	a crash between MarkCharging and Charge can be recovered via
//	gateway query. But two race windows still produce duplicate
//	Charge calls under at-least-once Kafka delivery:
//	  a) Charge succeeds, MarkConfirmed fails (DB blip), Kafka
//	     redelivers, the new worker sees status=Charging, calls
//	     reconciler? No — the worker SKIPS because the order isn't
//	     Pending. Recon would then call gateway.GetStatus, NOT
//	     Charge. So this race is closed by A4.
//	  b) MarkCharging succeeds, Charge call hangs past Kafka
//	     session timeout, partition rebalances, new worker sees
//	     Charging and skips. Recon resolves via GetStatus.
//	     Original Charge call eventually returns (success or
//	     failure), but the worker doing it has been killed by
//	     Kafka — its result is lost. Recon's result wins.
//	     Charge is never called twice in this race.
//
// So A4 narrows the duplicate-Charge windows but doesn't eliminate
// the requirement. A buggy adapter that doesn't send an idempotency
// key still wreaks havoc the moment a Kafka redelivery fires before
// MarkCharging commits.
type PaymentCharger interface {
	Charge(ctx context.Context, orderID uuid.UUID, amount float64) error
}

// PaymentStatusReader is the read side. The reconciler subcommand is
// the sole consumer; the payment-worker does NOT have this port in
// scope (it doesn't need to query — it owns the Charge call).
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
//                                             timeout). Reconciler treats
//                                             as transient: emit error
//                                             metric, retry next sweep.
//
// Note: returning `(Charged, err)` is permitted — the gateway might
// answer "yes charged" but the adapter's transport layer fails on
// the response read. Caller MUST inspect err first.
type PaymentStatusReader interface {
	GetStatus(ctx context.Context, orderID uuid.UUID) (ChargeStatus, error)
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
// is the sole consumer; the reconciler / saga / payment-worker do NOT
// receive this port.
//
// IDEMPOTENCY CONTRACT — same shape as PaymentCharger's:
// implementations MUST treat orderID as the gateway-side idempotency
// key. Repeat CreatePaymentIntent calls with the same orderID MUST
// return the SAME `PaymentIntent` value (same ID, same ClientSecret,
// same AmountCents, same Currency) — they MUST NOT register a second
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
// implement all three sides (Charge / GetStatus / CreatePaymentIntent).
// New consumers should depend on the narrowest port that fits their
// needs (PaymentCharger / PaymentStatusReader / PaymentIntentCreator).
// Kept here (not deleted) because MockGateway in the test infrastructure
// is the single adapter today and naturally implements all three —
// splitting the adapter into separate types just to satisfy a "narrow
// port at the adapter" rule would add ceremony without payoff.
//
// Real Stripe / Adyen / Braintree adapters in production would
// likewise implement multiple sides on a single struct.
//
// D7 cleanup: once the legacy A4 flow is decommissioned, PaymentCharger
// + PaymentStatusReader can be removed from the gateway entirely,
// leaving only PaymentIntentCreator (+ a future PaymentIntentReader
// for D5 webhook verification). The aggregated interface here makes
// that future shrink visible — D7 just removes embeds.
type PaymentGateway interface {
	PaymentCharger
	PaymentStatusReader
	PaymentIntentCreator
}
