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

// PaymentGateway is the legacy combined port retained for adapters
// that implement both sides. New consumers should depend on the
// narrower PaymentCharger or PaymentStatusReader directly. Kept here
// (not deleted) because MockGateway in the test infrastructure is the
// single adapter today and naturally implements both — splitting the
// adapter into two types just to satisfy a "narrow port at the
// adapter" rule would add ceremony without payoff.
//
// Real Stripe / Adyen / Braintree adapters in production would
// likewise implement both sides on a single struct.
type PaymentGateway interface {
	PaymentCharger
	PaymentStatusReader
}
