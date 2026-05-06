package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader is Stripe's webhook signature header name. Centralised
// so the handler doesn't carry the magic string and the mock signer
// can refer to the same constant.
const SignatureHeader = "Stripe-Signature"

// Typed errors so the handler can map to distinct HTTP codes / metric
// labels (`signature_invalid_total{reason}` per the alert plan).
//
// Why typed errors instead of a single ErrSignatureInvalid: an alert
// like "signature_invalid_total > 0.1/s" wants to differentiate a
// secret rotation drift (high `mismatch` rate, transient) from a
// genuine attacker (steady `format` errors) from a clock-skew issue
// at the provider edge (`skew_exceeded`). Operators read the label
// to triage; the handler maps each err to its label.
var (
	// ErrSignatureMissing fires when the request has no
	// `Stripe-Signature` header at all — usually a misrouted request
	// or a legitimate-looking call from someone who doesn't know the
	// surface. Handler maps to 401.
	ErrSignatureMissing = errors.New("webhook signature header missing")

	// ErrSignatureMalformed fires when the header is present but
	// can't be split into the expected `t=<unix>,v1=<hex>` shape
	// (or any required component is empty / not parseable).
	ErrSignatureMalformed = errors.New("webhook signature header malformed")

	// ErrSignatureSkewExceeded fires when `t=<unix>` is too far from
	// the verifier's `now`. Stripe documents 5 minutes as the
	// recommended tolerance; we make it injectable so tests can dial
	// it down to deterministic-second granularity.
	ErrSignatureSkewExceeded = errors.New("webhook signature timestamp outside tolerance")

	// ErrSignatureMismatch fires when the HMAC over `t.body` does not
	// match any v1 candidate. The legitimate cause is secret
	// rotation; the suspicious cause is forgery.
	ErrSignatureMismatch = errors.New("webhook signature mismatch")
)

// VerifySignature checks a Stripe-shape webhook signature header.
//
// Pure function — no side effects, no clock smuggling. Tests pass a
// fixed `now`; production code passes `time.Now()`.
//
// Inputs:
//   - secret    the webhook signing secret (provider dashboard /
//               env var `PAYMENT_WEBHOOK_SECRET`)
//   - body      the RAW request body bytes — MUST NOT be the result
//               of a parse + re-marshal round-trip (json.Marshal does
//               not preserve byte-for-byte, so the HMAC would
//               legitimately mismatch against the provider's
//               signature). Handler responsibility to read body once
//               via `io.ReadAll(io.LimitReader(...))` BEFORE
//               unmarshaling.
//   - header    the raw `Stripe-Signature` value
//   - now       caller-supplied wall clock (test seam)
//   - tolerance allowed skew between `t=<unix>` and `now`
//
// Returns nil on a valid signature; one of the typed errors above
// otherwise.
//
// The header format (Stripe convention):
//
//	t=1614000000,v1=abcdef123...,v0=optional_legacy_hash
//
// We support multiple v1 candidates (Stripe sends two during secret
// rotation; we accept either). v0 is the legacy MAC scheme that
// Stripe deprecates — we ignore it; if a customer is still on v0
// they need to upgrade.
//
// Security note: `hmac.Equal` is a constant-time comparison — using
// `==` on the hex strings would be a timing oracle.
func VerifySignature(secret []byte, body []byte, header string, now time.Time, tolerance time.Duration) error {
	if header == "" {
		return ErrSignatureMissing
	}
	if len(secret) == 0 {
		// Empty secret would silently make every signature valid
		// (HMAC of an empty key is well-defined and forgeable). Treat
		// as a config bug rather than a per-request invalid signature
		// so it surfaces loudly instead of as a flood of 401s.
		return fmt.Errorf("VerifySignature: empty secret (config bug)")
	}

	t, candidates, err := parseSignatureHeader(header)
	if err != nil {
		return err
	}

	// Replay protection — reject signatures whose `t` is too far from
	// our wall clock in either direction. Future-skew matters too
	// (a client with a wildly wrong clock could mint a header that
	// outlives the secret rotation window).
	skew := now.Sub(t)
	if skew < 0 {
		skew = -skew
	}
	if skew > tolerance {
		return fmt.Errorf("%w: skew=%s tolerance=%s", ErrSignatureSkewExceeded, skew, tolerance)
	}

	// Stripe's signed payload is `<t>.<body>` (decimal unix seconds,
	// dot, raw body bytes).
	mac := hmac.New(sha256.New, secret)
	if _, err := mac.Write(strconv.AppendInt(nil, t.Unix(), 10)); err != nil {
		// hmac.Write per the stdlib doc never returns an error;
		// surface defensively rather than ignore.
		return fmt.Errorf("VerifySignature: hmac write t: %w", err)
	}
	if _, err := mac.Write([]byte{'.'}); err != nil {
		return fmt.Errorf("VerifySignature: hmac write dot: %w", err)
	}
	if _, err := mac.Write(body); err != nil {
		return fmt.Errorf("VerifySignature: hmac write body: %w", err)
	}
	want := mac.Sum(nil)

	for _, v1 := range candidates {
		got, decErr := hex.DecodeString(v1)
		if decErr != nil || len(got) != len(want) {
			// Skip malformed individual candidates rather than failing
			// the whole header — the provider may include both a v1
			// from a current secret and a v1 from a rotating-out
			// secret; tolerating one bad encoding is safer.
			continue
		}
		if hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrSignatureMismatch
}

// parseSignatureHeader extracts `t` and the slice of v1 candidates
// from the comma-separated header. Returns ErrSignatureMalformed on
// any parse failure so the handler can surface a single 401 + metric
// label without duplicating cases.
func parseSignatureHeader(header string) (time.Time, []string, error) {
	var (
		tSec     int64 = -1
		v1Hashes []string
	)
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return time.Time{}, nil, fmt.Errorf("%w: missing '=' in part %q", ErrSignatureMalformed, part)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "t":
			parsed, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return time.Time{}, nil, fmt.Errorf("%w: t not unix-seconds: %v", ErrSignatureMalformed, err)
			}
			tSec = parsed
		case "v1":
			if v == "" {
				return time.Time{}, nil, fmt.Errorf("%w: empty v1", ErrSignatureMalformed)
			}
			v1Hashes = append(v1Hashes, v)
		// v0 / unknown keys: ignore. Stripe occasionally adds new
		// scheme tags; tolerating unknown keys keeps the verifier
		// forward-compatible.
		default:
		}
	}
	if tSec < 0 {
		return time.Time{}, nil, fmt.Errorf("%w: missing t", ErrSignatureMalformed)
	}
	if len(v1Hashes) == 0 {
		return time.Time{}, nil, fmt.Errorf("%w: missing v1", ErrSignatureMalformed)
	}
	return time.Unix(tSec, 0), v1Hashes, nil
}

// ClassifySignatureError maps a VerifySignature error to the metric
// label used by `payment_webhook_signature_invalid_total{reason}`.
// Centralised so the handler and the test harness produce identical
// labels.
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
	default:
		// Empty-secret config bug or hmac write failure.
		return "config_error"
	}
}
