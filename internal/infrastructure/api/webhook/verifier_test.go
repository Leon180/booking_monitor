package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"booking_monitor/internal/infrastructure/api/webhook"
)

// makeSig produces a Stripe-shape header. Test helper — production
// code never produces a signature header (the provider does).
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
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	tolerance := 5 * time.Minute

	t.Run("valid signature passes", func(t *testing.T) {
		header := makeSig(t, secret, body, now)
		require.NoError(t, webhook.VerifySignature(secret, body, header, now, tolerance))
	})

	t.Run("rotating secret: extra v1 candidate (one matches)", func(t *testing.T) {
		// Stripe sends two v1 hashes during rotation — one from the
		// active secret, one from the rolling-out secret.
		header := makeSig(t, secret, body, now, "deadbeef"+strings.Repeat("0", 56))
		require.NoError(t, webhook.VerifySignature(secret, body, header, now, tolerance))
	})

	t.Run("missing header → ErrSignatureMissing", func(t *testing.T) {
		err := webhook.VerifySignature(secret, body, "", now, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMissing)
		require.Equal(t, "missing", webhook.ClassifySignatureError(err))
	})

	t.Run("malformed header (no '=') → ErrSignatureMalformed", func(t *testing.T) {
		err := webhook.VerifySignature(secret, body, "garbage", now, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMalformed)
		require.Equal(t, "malformed", webhook.ClassifySignatureError(err))
	})

	t.Run("malformed header (missing t) → ErrSignatureMalformed", func(t *testing.T) {
		err := webhook.VerifySignature(secret, body, "v1=abc", now, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMalformed)
	})

	t.Run("malformed header (missing v1) → ErrSignatureMalformed", func(t *testing.T) {
		header := "t=" + strconv.FormatInt(now.Unix(), 10)
		err := webhook.VerifySignature(secret, body, header, now, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMalformed)
	})

	t.Run("skew exceeded (past) → ErrSignatureSkewExceeded", func(t *testing.T) {
		oldTime := now.Add(-10 * time.Minute)
		header := makeSig(t, secret, body, oldTime)
		err := webhook.VerifySignature(secret, body, header, now, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureSkewExceeded)
		require.Equal(t, "skew_exceeded", webhook.ClassifySignatureError(err))
	})

	t.Run("skew exceeded (future) → ErrSignatureSkewExceeded", func(t *testing.T) {
		futureTime := now.Add(10 * time.Minute)
		header := makeSig(t, secret, body, futureTime)
		err := webhook.VerifySignature(secret, body, header, now, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureSkewExceeded)
	})

	t.Run("hmac mismatch (wrong secret) → ErrSignatureMismatch", func(t *testing.T) {
		header := makeSig(t, []byte("wrong_secret"), body, now)
		err := webhook.VerifySignature(secret, body, header, now, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMismatch)
		require.Equal(t, "mismatch", webhook.ClassifySignatureError(err))
	})

	t.Run("hmac mismatch (body tampered) → ErrSignatureMismatch", func(t *testing.T) {
		header := makeSig(t, secret, body, now)
		tampered := []byte(`{"id":"evt_999","type":"payment_intent.succeeded"}`)
		err := webhook.VerifySignature(secret, tampered, header, now, tolerance)
		require.ErrorIs(t, err, webhook.ErrSignatureMismatch)
	})

	t.Run("empty secret → config error (loud)", func(t *testing.T) {
		header := makeSig(t, secret, body, now)
		err := webhook.VerifySignature(nil, body, header, now, tolerance)
		require.Error(t, err)
		require.NotErrorIs(t, err, webhook.ErrSignatureMismatch,
			"empty secret must not leak as a per-request signature failure")
		require.Equal(t, "config_error", webhook.ClassifySignatureError(err))
	})
}
