package payment

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// MockGateway simulates a payment provider (e.g., Stripe).
type MockGateway struct {
	// SuccessRate is the probability of a successful charge (0.0 - 1.0).
	SuccessRate float64
	// MinLatency is the minimum time to wait before responding.
	MinLatency time.Duration
	// MaxLatency is the maximum time to wait before responding.
	MaxLatency time.Duration
}

// NewMockGateway creates a new mock gateway with default settings.
func NewMockGateway() *MockGateway {
	return &MockGateway{
		SuccessRate: 0.95, // 95% success rate
		MinLatency:  50 * time.Millisecond,
		MaxLatency:  200 * time.Millisecond,
	}
}

// Charge simulates processing a payment.
func (g *MockGateway) Charge(ctx context.Context, orderID uuid.UUID, amount float64) error {
	// Simulate network latency
	// rand.Int64N returns a non-negative pseudo-random number in [0,n).
	latencyDelay := g.MaxLatency - g.MinLatency
	if latencyDelay <= 0 {
		latencyDelay = 1 // Safety
	}

	latency := g.MinLatency + time.Duration(rand.Int64N(int64(latencyDelay)))
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Simulate payment failures based on SuccessRate
	if rand.Float64() > g.SuccessRate {
		return errors.New("payment declined by mock gateway")
	}

	return nil
}

// Ensure MockGateway implements domain.PaymentGateway
var _ domain.PaymentGateway = (*MockGateway)(nil)
