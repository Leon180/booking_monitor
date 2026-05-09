package payment

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/domain"
)

// D7 (2026-05-08) deleted `MockGateway.Charge` along with
// `domain.PaymentGateway.Charge`. The pre-D7 idempotency-on-orderID
// tests for the `Charge` path are gone with it. The remaining contract
// surface this file pins is:
//
//   - `CreatePaymentIntent` is idempotent on orderID (same orderID →
//     same PaymentIntent value);
//   - `GetStatus` is a stub-shaped reader that always returns
//     `ChargeStatusNotFound` post-D7 (the mock has no charge history
//     to look up).

func TestMockGateway_CreatePaymentIntent_IsIdempotentOnOrderID(t *testing.T) {
	gw := NewMockGateway()
	gw.MinLatency = 0
	gw.MaxLatency = time.Microsecond
	orderID := uuid.New()

	first, err := gw.CreatePaymentIntent(context.Background(), orderID, 2000, "usd", nil)
	require.NoError(t, err)
	require.NotEmpty(t, first.ID, "intent ID must be populated")

	// Repeat call with the same orderID returns the cached intent.
	second, err := gw.CreatePaymentIntent(context.Background(), orderID, 2000, "usd", nil)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "repeat CreatePaymentIntent with same orderID must return cached ID")
	assert.Equal(t, first.ClientSecret, second.ClientSecret, "ClientSecret must also be cached")
}

func TestMockGateway_CreatePaymentIntent_RejectsInconsistentRetry(t *testing.T) {
	gw := NewMockGateway()
	gw.MinLatency = 0
	gw.MaxLatency = time.Microsecond
	orderID := uuid.New()

	_, err := gw.CreatePaymentIntent(context.Background(), orderID, 2000, "usd", nil)
	require.NoError(t, err)

	// A retry with different amount/currency should surface loudly
	// rather than silently returning the cached intent.
	_, err = gw.CreatePaymentIntent(context.Background(), orderID, 5000, "usd", nil)
	assert.Error(t, err, "inconsistent amount on retry must surface as an error, not silently return cached")
}

func TestMockGateway_CreatePaymentIntent_DifferentOrdersAreIndependent(t *testing.T) {
	gw := NewMockGateway()
	gw.MinLatency = 0
	gw.MaxLatency = time.Microsecond

	a, errA := gw.CreatePaymentIntent(context.Background(), uuid.New(), 2000, "usd", nil)
	require.NoError(t, errA)
	b, errB := gw.CreatePaymentIntent(context.Background(), uuid.New(), 2000, "usd", nil)
	require.NoError(t, errB)
	assert.NotEqual(t, a.ID, b.ID, "different orderIDs must produce different intents")
}

func TestMockGateway_CreatePaymentIntent_CtxCancellationDoesNotPoisonCache(t *testing.T) {
	gw := NewMockGateway()
	gw.MinLatency = 50 * time.Millisecond
	gw.MaxLatency = 100 * time.Millisecond
	orderID := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := gw.CreatePaymentIntent(ctx, orderID, 2000, "usd", nil)
	require.ErrorIs(t, err, context.Canceled)

	// A fresh ctx should still produce an intent — the cancelled call
	// must not have poisoned the cache.
	_, err = gw.CreatePaymentIntent(context.Background(), orderID, 2000, "usd", nil)
	assert.NoError(t, err, "cancelled-before-create call must leave cache unpoisoned")
}

func TestMockGateway_GetStatus_AlwaysNotFound(t *testing.T) {
	// Post-D7 the mock has no charge history; recon's NotFound branch
	// handles the rare stuck-Charging order it might still encounter
	// in transition.
	// D4.2: GetStatus signature changed from uuid.UUID to string
	// (Stripe wire shape). Mock impl is unaffected — input ignored.
	gw := NewMockGateway()
	got, err := gw.GetStatus(context.Background(), "pi_test_arbitrary")
	require.NoError(t, err)
	assert.Equal(t, domain.ChargeStatusNotFound, got)
}

func TestMockGateway_GetStatus_HonoursCtx(t *testing.T) {
	gw := NewMockGateway()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := gw.GetStatus(ctx, "pi_test_arbitrary")
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, domain.ChargeStatusUnknown, got, "cancelled ctx must surface as Unknown + ctx.Err per port contract")
}
