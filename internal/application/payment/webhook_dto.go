// Inbound webhook DTO for the D5 payment-provider surface.
//
// Lives in the application layer alongside the WebhookService that
// consumes it (mirrors how `OrderFailedEvent` — the outbound-async
// saga-compensation DTO — also lives in `application`). The HTTP edge
// in `infrastructure/api/webhook/` parses raw bytes into one of these
// and hands it to the service.
//
// The wire shape mirrors Stripe's `Event` object so a future swap
// from MockGateway to a real Stripe / Adyen / Braintree adapter only
// needs a different signature secret + URL; the parser, verifier,
// and service-layer dispatch stay byte-identical.
//
// Why re-implement Stripe's struct rather than import their SDK:
//   - The dependency is small so a vendored SDK is overkill for the
//     handful of fields we actually inspect.
//   - The mock provider in `internal/infrastructure/payment/` emits
//     the same shape directly without round-tripping through the SDK
//     so unit tests stay deterministic.
//   - Switching providers later means swapping the verifier (different
//     header / hash) and the field accessors — keeping our own struct
//     makes the boundary explicit.
package payment

// Envelope is the top-level payment-provider webhook payload —
// Stripe-shape so a future real adapter is a drop-in.
//
// Required fields (we hard-fail if absent on parse):
//   - ID        provider-issued event id; logged for traceability
//   - Type      discriminator the WebhookService dispatches on
//   - Data      the inner object the event describes
//
// Optional fields (D5 reads but tolerates absence):
//   - Created   unix-seconds the provider stamped the event;
//               cross-checked with `Stripe-Signature: t=<unix>` for
//               replay protection (verifier owns that check, not us).
//   - LiveMode  true on prod-mode webhooks, false on test-mode. The
//               WebhookService cross-checks against the configured
//               expected mode and 200-no-ops mismatches (cross-env
//               leak: a test-key signature reaching a prod listener
//               wastes provider retries if we return 5xx).
type Envelope struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Created  int64        `json:"created,omitempty"`
	LiveMode bool         `json:"livemode,omitempty"`
	Data     EnvelopeData `json:"data"`
}

// EnvelopeData wraps the inner object; matches Stripe's shape exactly.
// The wrapper exists so future event types that include `previous_attributes`
// (Stripe sends it on `*.updated` events) parse cleanly when we add them.
type EnvelopeData struct {
	Object PaymentIntentObject `json:"object"`
}

// PaymentIntentObject is the Stripe-shape PaymentIntent fields the D5
// handler reads. We are NOT modelling the full Stripe PaymentIntent —
// just the fields that drive a state-machine decision.
type PaymentIntentObject struct {
	// ID is the gateway-issued intent identifier ("pi_..."). Used as
	// the FALLBACK lookup key when the metadata.order_id primary path
	// fails (legacy intent or rescue from the SetPaymentIntentID race).
	ID string `json:"id"`

	// Status is Stripe's intent-side status string. We dispatch on
	// the `Envelope.Type` discriminator (`payment_intent.succeeded`
	// etc.), NOT on this field — Stripe's docs say type is the
	// canonical event-type signal and status can lag during state
	// transitions. Kept on the struct for log enrichment / debugging.
	Status string `json:"status,omitempty"`

	// AmountCents is the smallest-unit amount (Stripe convention).
	// Logged for audit but NOT compared with our order's
	// `amount_cents` — the gateway is the authority on what got
	// charged. A future "amount mismatch" check could surface here.
	AmountCents int64 `json:"amount,omitempty"`

	// Currency is the ISO 4217 lowercase code. Same audit posture as
	// AmountCents: log, don't compare.
	Currency string `json:"currency,omitempty"`

	// Metadata is the caller-controlled attribute bag. D4 `/pay`
	// writes `{"order_id": "<uuid>"}` into this map when creating
	// the intent; D5 reads `metadata["order_id"]` as the PRIMARY
	// lookup key. Empty / missing falls back to ID lookup —
	// guaranteeing we still land on the right order even after a
	// SetPaymentIntentID write failure.
	Metadata map[string]string `json:"metadata,omitempty"`

	// LastPaymentError carries the provider's failure reason on
	// `payment_intent.payment_failed` events. Threaded into the
	// `order.failed` saga event's Reason field for operator triage.
	// Pointer + omitempty so successful events parse without a
	// dangling empty error block.
	LastPaymentError *PaymentIntentLastError `json:"last_payment_error,omitempty"`
}

// PaymentIntentLastError captures the provider's failure context.
// Stripe sends a richer object (charge id, decline code, payment-method
// type, network risk score, etc.) — we copy only the two fields the
// saga event currently displays. Adding more is purely additive on
// JSON parse; nothing breaks if the provider sends extra keys.
type PaymentIntentLastError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Provider-side event types we explicitly handle. Anything else
// surfaces as an `unsupported_type` 200 no-op (the provider should
// stop retrying; we shouldn't error a class of events we don't
// understand into the retry storm).
const (
	EventTypePaymentIntentSucceeded     = "payment_intent.succeeded"
	EventTypePaymentIntentPaymentFailed = "payment_intent.payment_failed"
	// `payment_intent.canceled` is intentionally NOT handled in D5
	// (out-of-scope per plan v5 §Out-of-scope). Falls through to
	// `unsupported_type` until the follow-up PR distinguishes
	// provider-side cancel vs user-side abandon.
)

// MetadataKeyOrderID is the metadata field name for the order UUID.
// Centralised so producer (`/pay`) and consumer (webhook handler)
// can never drift. Shared by the mock webhook signer in
// `internal/infrastructure/payment/`.
const MetadataKeyOrderID = "order_id"
