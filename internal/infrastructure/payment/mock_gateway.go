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

	// intents caches the first-call PaymentIntent per orderID for the
	// CreatePaymentIntent path. Mirrors `results` in shape: subsequent
	// calls with the same orderID return the cached intent verbatim,
	// honouring the PaymentIntentCreator idempotency contract that
	// real Stripe / Adyen adapters enforce via their `Idempotency-Key`
	// header.
	intents sync.Map // map[uuid.UUID]domain.PaymentIntent
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

// GetStatus returns the cached verdict for orderID as a typed
// ChargeStatus. Implements the PaymentStatusReader port that the
// recon subcommand uses to resolve stuck-Charging orders.
//
// Mapping from the in-memory results sync.Map to ChargeStatus:
//
//	cached value nil     → ChargeStatusCharged   (Charge returned nil)
//	cached value non-nil → ChargeStatusDeclined  (Charge returned a payment-declined error)
//	no entry             → ChargeStatusNotFound  (Charge was never called for this orderID)
//
// A real Stripe/Adyen adapter would call `GET /v1/charges/{id}` and
// translate the provider's response field to ChargeStatus. The MockGateway
// internal model is one-to-one because the result cache IS the
// "provider's response" in this simulation.
//
// Why ctx-cancelled Charge calls don't appear here as a distinct
// status: the Charge implementation deliberately does NOT cache
// ctx-cancelled outcomes (line 74-77 above). From GetStatus's
// perspective, a ctx-cancelled prior Charge looks identical to "Charge
// was never called" → returns NotFound. This matches the recon's
// recovery model: NotFound = retry the Charge (or transition to
// Failed if max-age exhausted), and ctx-cancelled is exactly that
// scenario.
//
// GetStatus itself never blocks — it's a sync.Map.Load. The ctx is
// honored for symmetry with the port contract; a real adapter's
// HTTP call respects it.
func (g *MockGateway) GetStatus(ctx context.Context, orderID uuid.UUID) (domain.ChargeStatus, error) {
	// Honor ctx even on a cheap path so callers can rely on the
	// timeout boundary. Returning ChargeStatusUnknown + ctx.Err()
	// is the documented "transient infra failure" branch — the
	// reconciler treats it as "skip this order, retry next sweep".
	if err := ctx.Err(); err != nil {
		return domain.ChargeStatusUnknown, err
	}

	cached, ok := g.results.Load(orderID)
	if !ok {
		return domain.ChargeStatusNotFound, nil
	}
	if cached == nil {
		return domain.ChargeStatusCharged, nil
	}
	// Non-nil cached error means a declined verdict from Charge.
	// We don't surface the underlying error string — ChargeStatus is
	// the wire vocabulary; the reason lives in logs / order_status_history.
	return domain.ChargeStatusDeclined, nil
}

// CreatePaymentIntent simulates the Stripe-shape PaymentIntent
// creation flow. Returns the same PaymentIntent on repeat calls with
// the same orderID — the gateway-side idempotency contract that
// makes /pay (D4) safe to retry without an application-layer cache.
//
// The intent ID + ClientSecret are randomly generated on the first
// call and cached. Real Stripe IDs are short opaque strings of the
// form `pi_<26 hex chars>`; we mimic that with the orderID's hex
// (without dashes) as a deterministic seed so logs / DB rows are
// easy to correlate by hand. ClientSecret follows Stripe's
// `pi_<intent>_secret_<random>` convention.
//
// Why ctx is honoured even on a cheap (cache hit) path: same
// rationale as GetStatus — a real adapter would call
// `POST /v1/payment_intents` over HTTP and respect ctx.Done().
// Caller writes propagate the timeout boundary; we don't break it.
func (g *MockGateway) CreatePaymentIntent(ctx context.Context, orderID uuid.UUID, amountCents int64, currency string) (domain.PaymentIntent, error) {
	if err := ctx.Err(); err != nil {
		return domain.PaymentIntent{}, err
	}

	// Idempotency short-circuit: same orderID → same intent. Stripe
	// behaviour. The amount/currency from this call are validated
	// against the cached intent so a buggy caller passing different
	// amounts on retry surfaces loudly rather than silently using the
	// first call's amount.
	if cached, ok := g.intents.Load(orderID); ok {
		intent := cached.(domain.PaymentIntent)
		if intent.AmountCents != amountCents || intent.Currency != currency {
			return domain.PaymentIntent{}, errors.New("CreatePaymentIntent: cached intent has different amount/currency — caller passed inconsistent params on retry")
		}
		return intent, nil
	}

	// First-time creation. Mock latency analogous to Charge so /pay
	// p95 numbers in benchmarks aren't unrealistically fast.
	latencyDelay := g.MaxLatency - g.MinLatency
	if latencyDelay <= 0 {
		latencyDelay = 1
	}
	latency := g.MinLatency + time.Duration(rand.Int64N(int64(latencyDelay))) //nolint:gosec // G404 — math/rand is correct for simulated latency in a mock
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		// Don't cache on cancellation — same rule as Charge.
		return domain.PaymentIntent{}, ctx.Err()
	}

	// Mock IDs derived deterministically from orderID so a developer
	// reading the DB / logs can correlate without a lookup table.
	// Real Stripe IDs are random; this is mock-specific.
	orderHex := orderID.String()
	intent := domain.PaymentIntent{
		ID:           "pi_" + orderHex,
		ClientSecret: "pi_" + orderHex + "_secret_" + uuid.NewString(),
		AmountCents:  amountCents,
		Currency:     currency,
	}
	if actual, loaded := g.intents.LoadOrStore(orderID, intent); loaded {
		// Race: another goroutine stored first. Return their value
		// for consistency — both should be byte-identical except for
		// ClientSecret's random tail (which is fine; both are valid
		// secrets for the same intent in our mock model).
		return actual.(domain.PaymentIntent), nil
	}
	return intent, nil
}

// Ensure MockGateway implements all three port halves AND the combined
// legacy interface. Compile-time check — the assignment fails to
// type-check if any required method is missing.
var (
	_ domain.PaymentCharger        = (*MockGateway)(nil)
	_ domain.PaymentStatusReader   = (*MockGateway)(nil)
	_ domain.PaymentIntentCreator  = (*MockGateway)(nil)
	_ domain.PaymentGateway        = (*MockGateway)(nil)
)
