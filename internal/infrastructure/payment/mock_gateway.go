package payment

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// MockGateway simulates a payment provider (Stripe / Square / Adyen).
//
// IDEMPOTENT on orderID, per the domain.PaymentGateway contract: the
// FIRST Charge call for a given orderID computes a result (success or
// failure) under the configured SuccessRate roll, caches it in
// `results`, and every subsequent call with the same orderID short-
// circuits to the cached value — no second random roll, no second
// latency simulation. This mirrors how a real provider treats an
// `Idempotency-Key` header: same key → same cached response.
//
// The cache is in-memory and unbounded; for a mock that's fine. A
// real adapter would lean on the provider's server-side cache plus
// (optionally) a local short-TTL cache to avoid network round-trips.
type MockGateway struct {
	// SuccessRate is the probability of a successful charge (0.0 - 1.0).
	SuccessRate float64
	// MinLatency is the minimum time to wait before responding.
	MinLatency time.Duration
	// MaxLatency is the maximum time to wait before responding.
	MaxLatency time.Duration

	// results caches the first-call outcome per orderID. nil error means
	// success; non-nil means failed (and the SAME error is returned on
	// every retry — callers must see a stable verdict).
	results sync.Map // map[uuid.UUID]error
}

// NewMockGateway creates a new mock gateway with default settings.
func NewMockGateway() *MockGateway {
	return &MockGateway{
		SuccessRate: 0.95, // 95% success rate
		MinLatency:  50 * time.Millisecond,
		MaxLatency:  200 * time.Millisecond,
	}
}

// Charge simulates processing a payment with idempotent semantics.
func (g *MockGateway) Charge(ctx context.Context, orderID uuid.UUID, amount float64) error {
	// Idempotency short-circuit: if we've seen this orderID before,
	// return the cached verdict without latency or a second random
	// roll. This is the contract the payment service relies on to
	// avoid double-charging on Kafka redelivery.
	if cached, ok := g.results.Load(orderID); ok {
		if cached == nil {
			return nil
		}
		return cached.(error)
	}

	// Simulate network latency on the first call only.
	// rand.Int64N returns a non-negative pseudo-random number in [0,n).
	latencyDelay := g.MaxLatency - g.MinLatency
	if latencyDelay <= 0 {
		latencyDelay = 1 // Safety
	}
	latency := g.MinLatency + time.Duration(rand.Int64N(int64(latencyDelay))) //nolint:gosec // G404 — math/rand is correct for simulated latency in a mock; crypto/rand would be misleading
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		// Ctx cancellation does NOT cache — the request never reached
		// the simulated provider, so a retry with a non-cancelled ctx
		// still gets a fresh roll.
		return ctx.Err()
	}

	// First-time roll. Cache the verdict before returning so concurrent
	// retries see a consistent answer.
	var verdict error
	if rand.Float64() > g.SuccessRate { //nolint:gosec // G404 — math/rand is correct for simulating a deterministic-failure-rate gateway in tests
		verdict = errors.New("payment declined by mock gateway")
	}
	// LoadOrStore handles the rare race where two goroutines pass the
	// initial Load check concurrently — only the first stored value
	// wins, and we return that one for consistency.
	if actual, loaded := g.results.LoadOrStore(orderID, verdict); loaded {
		if actual == nil {
			return nil
		}
		return actual.(error)
	}
	return verdict
}

// Ensure MockGateway implements domain.PaymentGateway
var _ domain.PaymentGateway = (*MockGateway)(nil)
