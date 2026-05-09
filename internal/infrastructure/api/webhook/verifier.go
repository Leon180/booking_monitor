package webhook

// D4.2 Slice 3b. Internals delegate to stripe-go v82's webhook
// package — same crypto, same header parsing, same secret-rotation
// handling — instead of the hand-rolled HMAC pipeline. This package
// keeps the typed-error sentinels + ClassifySignatureError so the
// handler + alert taxonomy
// (`payment_webhook_signature_invalid_total{reason}`) does NOT
// change shape under the upgrade. The translation layer means a
// future stripe-go rename (v83+) cannot silently break our alert
// rules either.

import (
	"errors"
	"fmt"
	"time"

	stripewebhook "github.com/stripe/stripe-go/v82/webhook"
)

// SignatureHeader is Stripe's webhook signature header name. Centralised
// so the handler doesn't carry the magic string and the mock signer
// can refer to the same constant.
const SignatureHeader = "Stripe-Signature"

// Typed errors so the handler can map to distinct HTTP codes / metric
// labels (`signature_invalid_total{reason}`).
//
// Why typed errors instead of a single ErrSignatureInvalid: an alert
// like "signature_invalid_total > 0.1/s" wants to differentiate a
// secret rotation drift (high `mismatch` rate, transient) from a
// genuine attacker (steady `format` errors) from a clock-skew issue
// at the provider edge (`skew_exceeded`). Operators read the label
// to triage; the handler maps each err to its label.
//
// Mapping (stripe-go v82 → our taxonomy):
//
//	stripewebhook.ErrNotSigned        → ErrSignatureMissing
//	stripewebhook.ErrInvalidHeader    → ErrSignatureMalformed
//	stripewebhook.ErrTooOld           → ErrSignatureSkewExceeded
//	stripewebhook.ErrNoValidSignature → ErrSignatureMismatch
//
// Note (label collapse): stripe-go folds "no v1 in header" AND
// "v1 didn't match the HMAC" into the single ErrNoValidSignature.
// The pre-3b verifier had two finer labels — "missing v1" was
// `malformed`, "v1 didn't match" was `mismatch`. Post-3b, both
// surface as `mismatch`. This is judged acceptable because (a) the
// Stripe SDK reference is the authoritative shape we're aligning
// to, (b) "no v1 in a Stripe-Signature header" is itself a forgery
// signal in practice, and (c) ops alerts are written against the
// `mismatch` label as the primary signature-failure indicator
// regardless.
//
// Note (future-skew): stripe-go enforces only past-skew via
// `time.Since(t) > tolerance`. Future-skewed timestamps (a header
// `t=` ahead of the verifier's wall clock) are accepted as long as
// the HMAC verifies. The pre-3b verifier rejected both directions
// symmetrically; we now accept future-skew to match the Stripe SDK
// reference. A future-skewed signature still requires the secret
// to verify, so the residual risk is bounded — and a forged
// signature with the secret already implies compromise.
var (
	// ErrSignatureMissing fires when the request has no
	// `Stripe-Signature` header at all — usually a misrouted request
	// or a legitimate-looking call from someone who doesn't know the
	// surface. Handler maps to 401.
	ErrSignatureMissing = errors.New("webhook signature header missing")

	// ErrSignatureMalformed fires when the header is present but
	// can't be parsed into the expected `t=<unix>,v1=<hex>` shape
	// (any non `key=value` pair). Stripe-go's parseSignatureHeader
	// returns ErrInvalidHeader for this case.
	ErrSignatureMalformed = errors.New("webhook signature header malformed")

	// ErrSignatureSkewExceeded fires when `t=<unix>` is too far in
	// the past relative to the verifier's wall clock. Stripe
	// documents 5 minutes as the recommended tolerance; we accept
	// it as a parameter so tests can dial it down to deterministic-
	// second granularity.
	ErrSignatureSkewExceeded = errors.New("webhook signature timestamp outside tolerance")

	// ErrSignatureMismatch fires when the HMAC over `t.body` does
	// not match any v1 candidate, OR when the header carries no v1
	// candidates at all (see "label collapse" note above). The
	// legitimate cause is secret rotation lag; the suspicious cause
	// is forgery.
	ErrSignatureMismatch = errors.New("webhook signature mismatch")

	// ErrConfigError is the sentinel for verifier-level configuration
	// bugs surfaced at request time — currently only "empty secret",
	// but reserved for any future pre-flight check that fails for
	// a config-bug reason rather than a per-request reason.
	//
	// Distinct from the four taxonomy sentinels above because
	// `ClassifySignatureError` routes it to a different alert label
	// (`config_error`, page-worthy) and the handler maps it to HTTP
	// 500 (vs 401 for the per-request sentinels). Wrapping the
	// pre-flight `fmt.Errorf` with this sentinel lets ops + tests
	// assert `errors.Is(err, ErrConfigError)` directly instead of
	// reaching for the indirect `ClassifySignatureError(err) ==
	// "config_error"` check (Slice 3b multi-agent review feedback).
	ErrConfigError = errors.New("webhook verifier misconfigured")
)

// VerifySignature checks a Stripe-shape webhook signature header.
//
// Inputs:
//   - secret    the webhook signing secret (provider dashboard /
//               env var `STRIPE_WEBHOOK_SECRET` /
//               `PAYMENT_WEBHOOK_SECRET`)
//   - body      the RAW request body bytes — MUST NOT be the result
//               of a parse + re-marshal round-trip (json.Marshal does
//               not preserve byte-for-byte, so the HMAC would
//               legitimately mismatch against the provider's
//               signature). Handler responsibility to read body once
//               via `io.ReadAll(io.LimitReader(...))` BEFORE
//               unmarshaling.
//   - header    the raw `Stripe-Signature` value
//   - tolerance allowed past-skew between `t=<unix>` and `time.Now()`.
//               Pass stripewebhook.DefaultTolerance (5min) in
//               production; tests may pass shorter for deterministic
//               assertions.
//
// Returns nil on a valid signature; one of the typed errors above
// otherwise.
//
// Internals: after two pre-flight checks (empty header, empty
// secret), delegates to `stripewebhook.ValidatePayloadWithTolerance`.
// We keep `VerifySignature` as a thin facade so the rest of the
// handler doesn't import stripe-go (stripe-go is a fairly heavy
// transitive dependency tree we'd rather isolate to the
// infrastructure boundary).
//
// Security note: the underlying stripe-go verification uses
// `hmac.Equal` (constant-time) — the timing-oracle concern is
// handled correctly upstream.
func VerifySignature(secret []byte, body []byte, header string, tolerance time.Duration) error {
	if header == "" {
		// Pre-empt stripe-go's ErrNotSigned so callers get our typed
		// sentinel directly without an extra mapStripe call. (Both
		// paths produce the same final sentinel either way; the
		// pre-empt just keeps the error message stable for any log
		// matchers / alerts that grep on it.)
		return ErrSignatureMissing
	}
	if len(secret) == 0 {
		// Empty secret would silently make every signature valid
		// (HMAC of an empty key is well-defined and forgeable). Treat
		// as a config bug rather than a per-request invalid signature
		// so it surfaces loudly instead of as a flood of 401s.
		return fmt.Errorf("VerifySignature: empty secret: %w", ErrConfigError)
	}

	if err := stripewebhook.ValidatePayloadWithTolerance(body, header, string(secret), tolerance); err != nil {
		return mapStripeWebhookError(err)
	}
	return nil
}

// mapStripeWebhookError translates stripe-go v82's webhook errors to
// our typed sentinels.
//
// Uses errors.Join (Go 1.20+) so the returned error matches BOTH
// our sentinel AND the underlying stripe-go error under errors.Is.
// That keeps `errors.Is(err, ErrSignatureMismatch)` working at the
// metric/handler boundary AND `errors.Is(err, stripewebhook.X)`
// working for deeper debugging without forcing callers to learn
// stripe-go's taxonomy.
//
// Same pattern as stripe_gateway.go's mapStripeError — keeps the
// translation idiom uniform across both adapter surfaces.
func mapStripeWebhookError(err error) error {
	switch {
	case errors.Is(err, stripewebhook.ErrNotSigned):
		return errors.Join(ErrSignatureMissing, err)
	case errors.Is(err, stripewebhook.ErrInvalidHeader):
		return errors.Join(ErrSignatureMalformed, err)
	case errors.Is(err, stripewebhook.ErrTooOld):
		return errors.Join(ErrSignatureSkewExceeded, err)
	case errors.Is(err, stripewebhook.ErrNoValidSignature):
		return errors.Join(ErrSignatureMismatch, err)
	default:
		// Defensive: any future stripe-go variant we haven't mapped
		// surfaces with a clear "unmapped variant" prefix and the
		// underlying stripe-go error preserved via %w. The
		// ClassifySignatureError default branch routes it to
		// `config_error`, which the alert rule treats as page-worthy
		// — operators searching the log see the prefix and know to
		// add a new mapping. Without this prefix the log line was
		// indistinguishable from raw stripe-go internals (Slice 3b
		// multi-agent review feedback).
		return fmt.Errorf("unmapped stripe-go webhook error: %w", err)
	}
}

// ClassifySignatureError maps a VerifySignature error to the metric
// label used by `payment_webhook_signature_invalid_total{reason}`.
// Centralised so the handler and the test harness produce identical
// labels.
//
// `config_error` is matched explicitly via ErrConfigError AND falls
// through on the default branch — the explicit match is for the
// known empty-secret pre-flight, the default is the safety net for
// any future unmapped stripe-go variant. Both routes converge on the
// same `config_error` label so the alert rule keeps working in either
// case.
func ClassifySignatureError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSignatureMissing):
		return "missing"
	case errors.Is(err, ErrSignatureMalformed):
		return "malformed"
	case errors.Is(err, ErrSignatureSkewExceeded):
		return "skew_exceeded"
	case errors.Is(err, ErrSignatureMismatch):
		return "mismatch"
	case errors.Is(err, ErrConfigError):
		return "config_error"
	default:
		// Unmapped stripe-go variant — see mapStripeWebhookError's
		// default branch comment.
		return "config_error"
	}
}
