package payment

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMockGateway_IsIdempotentOnOrderID directly verifies the
// `domain.PaymentGateway` idempotency contract: repeat Charge calls
// with the same orderID return the result of the FIRST call without
// re-rolling against SuccessRate. This is the property that lets
// payment.Service safely retry under Kafka redelivery.
func TestMockGateway_IsIdempotentOnOrderID(t *testing.T) {
	t.Run("cached failure stays a failure even when SuccessRate flips to 1.0", func(t *testing.T) {
		gw := &MockGateway{
			SuccessRate: 0.0, // first call MUST fail
			MinLatency:  0,
			MaxLatency:  time.Microsecond,
		}
		orderID := uuid.New()

		first := gw.Charge(context.Background(), orderID, 100)
		require.Error(t, first, "SuccessRate=0 should fail")

		// Flip to 1.0 — without idempotency the next call would succeed.
		// With idempotency, the cached failure persists.
		gw.SuccessRate = 1.0

		second := gw.Charge(context.Background(), orderID, 100)
		assert.Equal(t, first.Error(), second.Error(),
			"second call MUST return the cached failure verdict, not re-roll")
	})

	t.Run("cached success stays a success even when SuccessRate flips to 0.0", func(t *testing.T) {
		gw := &MockGateway{
			SuccessRate: 1.0,
			MinLatency:  0,
			MaxLatency:  time.Microsecond,
		}
		orderID := uuid.New()

		require.NoError(t, gw.Charge(context.Background(), orderID, 100))

		gw.SuccessRate = 0.0
		assert.NoError(t, gw.Charge(context.Background(), orderID, 100),
			"second call MUST return the cached success verdict, not re-roll")
	})

	t.Run("different orderIDs are independent", func(t *testing.T) {
		gw := &MockGateway{
			SuccessRate: 1.0,
			MinLatency:  0,
			MaxLatency:  time.Microsecond,
		}
		a, b := uuid.New(), uuid.New()
		assert.NoError(t, gw.Charge(context.Background(), a, 100))
		assert.NoError(t, gw.Charge(context.Background(), b, 100),
			"a fresh orderID must NOT inherit the cached verdict of a different orderID")
	})

	t.Run("ctx cancellation does not poison the cache", func(t *testing.T) {
		// MinLatency long enough that ctx cancellation triggers BEFORE the verdict roll.
		gw := &MockGateway{
			SuccessRate: 1.0,
			MinLatency:  50 * time.Millisecond,
			MaxLatency:  100 * time.Millisecond,
		}
		orderID := uuid.New()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		err := gw.Charge(ctx, orderID, 100)
		require.ErrorIs(t, err, context.Canceled, "cancelled call must surface ctx.Err")

		// A fresh ctx should still produce a verdict — the earlier cancelled
		// call must NOT have stored anything in the cache.
		assert.NoError(t, gw.Charge(context.Background(), orderID, 100),
			"cancelled-before-verdict call must leave the cache unpoisoned")
	})
}
