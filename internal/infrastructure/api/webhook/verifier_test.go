package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	stripewebhook "github.com/stripe/stripe-go/v82/webhook"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/infrastructure/api/webhook"
)

// makeSig produces a Stripe-shape header. Test helper — production
// code never produces a signature header (the provider does).
//
// D4.2 Slice 3b note: tests sign with real wall clock (time.Now()
// or time.Now().Add(-X) for skew tests) because the verifier was
// upgraded to delegate to stripe-go's webhook package, which uses
// `time.Now()` internally without a clock-injection seam. Skew
// assertions therefore use offsets from now() rather than fixed
// dates.
func makeSig(t *testing.T, secret []byte, body []byte, ts time.Time, extraV1 ...string) string {
	t.Helper()
	mac := hmac.New(sha256.New, secret)
	mac.Write(strconv.AppendInt(nil, ts.Unix(), 10))
	mac.Write([]byte{'.'})
	mac.Write(body)
	v1 := hex.EncodeToString(mac.Sum(nil))
	header := "t=" + strconv.FormatInt(ts.Unix(), 10) + ",v1=" + v1
	for _, extra := range extraV1 {
		header += ",v1=" + extra
	}
	return header
}

func TestVerifySignature(t *testing.T) {
	secret := []byte("whsec_test_secret_value")
	body := []byte(`{"id":"evt_123","type":"payment_intent.succeeded"}`)
	tolerance := 5 * time.Minute

	t.Run("valid signature passes", func(t *testing.T) {
		now := time.Now()
		header := makeSig(t, secret, body, now)
		require.NoError(t, webhook.VerifySignature(secret, body, header, tolerance))
	})

	t.Run("rotating secret: extra v1 candidate (one matches)", func(t *testing.T) {
		// Stripe sends two v1 hashes during rotation — one from the
		// active secret, one from the rolling-out secret. stripe-go
		// (and our delegated verifier) walk all v1 candidates and
		// accept on the first match.
		now := time.Now()
		header := makeSig(t, secret, body, now, "deadbeef"+strings.Repeat("0", 56))
		require.NoError(t, webhook.VerifySignature(secret, body, header, tolerance))
	})

	t.Run("missing header → ErrSignatureMissing", func(t *testing.T) {
		err := webhook.VerifySignature(secret, body, "", tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMissing)
		require.Equal(t, "missing", webhook.ClassifySignatureError(err))
	})

	t.Run("malformed header (no '=') → ErrSignatureMalformed", func(t *testing.T) {
		// stripe-go's parseSignatureHeader returns ErrInvalidHeader
		// when a comma-split part can't be `key=value` cut — that's
		// our "malformed" label.
		err := webhook.VerifySignature(secret, body, "garbage", tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMalformed)
		require.Equal(t, "malformed", webhook.ClassifySignatureError(err))
		// Wrapping preserves errors.Is on the underlying stripe-go
		// error too — useful for deeper debugging.
		require.ErrorIs(t, err, stripewebhook.ErrInvalidHeader)
	})

	t.Run("missing t (only v1 in header) → ErrSignatureSkewExceeded", func(t *testing.T) {
		// Verified against stripe-go v82.5.1 webhook/client.go:
		// parseSignatureHeader leaves `timestamp` at its zero value
		// when no `t=` field is present; validatePayload then evaluates
		// `time.Since(zeroTime) > tolerance` which is always true,
		// returning `ErrTooOld`. We map that to ErrSignatureSkewExceeded.
		// If a future stripe-go release (v83+) reorders this and
		// returns ErrInvalidHeader instead, this test will fail loudly
		// and we should re-classify intentionally.
		err := webhook.VerifySignature(secret, body, "v1=ab", tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureSkewExceeded)
		require.Equal(t, "skew_exceeded", webhook.ClassifySignatureError(err))
	})

	t.Run("missing v1 (only t in header) → ErrSignatureMismatch", func(t *testing.T) {
		// stripe-go: "no v1 in header" returns ErrNoValidSignature.
		// We map that to ErrSignatureMismatch (label collapse —
		// see verifier.go header doc note).
		header := "t=" + strconv.FormatInt(time.Now().Unix(), 10)
		err := webhook.VerifySignature(secret, body, header, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMismatch)
		require.Equal(t, "mismatch", webhook.ClassifySignatureError(err))
	})

	t.Run("skew exceeded (past) → ErrSignatureSkewExceeded", func(t *testing.T) {
		// 10 minutes in the past, tolerance is 5 minutes.
		oldTime := time.Now().Add(-10 * time.Minute)
		header := makeSig(t, secret, body, oldTime)
		err := webhook.VerifySignature(secret, body, header, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureSkewExceeded)
		require.Equal(t, "skew_exceeded", webhook.ClassifySignatureError(err))
		require.ErrorIs(t, err, stripewebhook.ErrTooOld)
	})

	t.Run("future-skew is ACCEPTED (Stripe SDK behavior)", func(t *testing.T) {
		// D4.2 Slice 3b behavior change: pre-3b verifier rejected
		// future-skewed timestamps symmetrically with past-skew.
		// Post-3b delegates to stripe-go which checks only
		// `time.Since(t) > tolerance`; future timestamps yield a
		// negative duration and pass the tolerance gate. The HMAC
		// itself still has to verify, so a forged future-skewed
		// signature would still be rejected at the mismatch stage.
		// Documented as an explicit alignment with the Stripe
		// reference SDK.
		futureTime := time.Now().Add(2 * time.Minute)
		header := makeSig(t, secret, body, futureTime)
		require.NoError(t, webhook.VerifySignature(secret, body, header, tolerance))
	})

	t.Run("hmac mismatch (wrong secret) → ErrSignatureMismatch", func(t *testing.T) {
		now := time.Now()
		header := makeSig(t, []byte("wrong_secret"), body, now)
		err := webhook.VerifySignature(secret, body, header, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMismatch)
		require.Equal(t, "mismatch", webhook.ClassifySignatureError(err))
		require.ErrorIs(t, err, stripewebhook.ErrNoValidSignature)
	})

	t.Run("hmac mismatch (body tampered) → ErrSignatureMismatch", func(t *testing.T) {
		now := time.Now()
		header := makeSig(t, secret, body, now)
		tampered := []byte(`{"id":"evt_999","type":"payment_intent.succeeded"}`)
		err := webhook.VerifySignature(secret, tampered, header, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMismatch)
	})

	t.Run("empty secret → ErrConfigError (loud)", func(t *testing.T) {
		now := time.Now()
		header := makeSig(t, secret, body, now)
		err := webhook.VerifySignature(nil, body, header, tolerance)
		require.ErrorIs(t, err, webhook.ErrConfigError,
			"empty secret must surface the typed ErrConfigError sentinel")
		require.NotErrorIs(t, err, webhook.ErrSignatureMismatch,
			"empty secret must not leak as a per-request signature failure")
		require.Equal(t, "config_error", webhook.ClassifySignatureError(err))
	})
}
