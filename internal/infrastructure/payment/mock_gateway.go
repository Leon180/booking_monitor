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
// Pre-D7 the mock also simulated the legacy A4 `Charge` path — a
// configurable success-rate roll, cached idempotency on orderID, etc.
// D7 (2026-05-08) deleted `domain.PaymentGateway.Charge` along with
// the auto-charge worker, so this mock now exposes only the Pattern A
// surfaces:
//
//   - `CreatePaymentIntent` (D4 entry point — the /pay handler), with
//     gateway-side idempotency (same orderID → same PaymentIntent) per
//     the `domain.PaymentIntentCreator` contract;
//   - `GetStatus` (recon's stuck-Charging probe) — kept as a
//     stub-shaped reader; with no `Charge` history to look up, every
//     call returns `ChargeStatusNotFound`. Recon's NotFound branch
//     handles this gracefully (see `recon.resolve`).
//
// The intents cache is in-memory and unbounded; for a mock that's fine.
// A real adapter would lean on the provider's server-side cache plus
// (optionally) a local short-TTL cache to avoid network round-trips.
type MockGateway struct {
	// MinLatency is the minimum time to wait before responding.
	MinLatency time.Duration
	// MaxLatency is the maximum time to wait before responding.
	MaxLatency time.Duration

	// intents caches the first-call PaymentIntent per orderID for the
	// CreatePaymentIntent path. Subsequent calls with the same orderID
	// return the cached intent verbatim, honouring the
	// PaymentIntentCreator idempotency contract that real Stripe / Adyen
	// adapters enforce via their `Idempotency-Key` header.
	intents sync.Map // map[uuid.UUID]domain.PaymentIntent

	// intentMetadataMu serialises metadata-edit-on-cache-hit. Without
	// it two concurrent CreatePaymentIntent calls with different
	// metadata could race on the `Load(copy) → mutate → Store`
	// sequence; the lock makes the read+write effectively atomic for
	// the duration of the mutation. Coarse-grained (one mutex for
	// the whole map) — fine for a mock; a real adapter would not
	// have this concern because metadata edits go through the
	// gateway API.
	intentMetadataMu sync.Mutex
}

// NewMockGateway creates a new mock gateway with default settings.
func NewMockGateway() *MockGateway {
	return &MockGateway{
		MinLatency: 50 * time.Millisecond,
		MaxLatency: 200 * time.Millisecond,
	}
}

// GetStatus implements `domain.PaymentStatusReader` for the recon
// subcommand. Pre-D7 this returned the cached `Charge` verdict (mapped
// from the in-memory results sync.Map). Post-D7 there is no `Charge`
// path on the mock — every call returns `ChargeStatusNotFound`.
//
// Recon's resolve loop treats NotFound as "no gateway record after
// threshold; transition to Failed with reason='recon: gateway has no
// charge record'", which is the correct outcome under Pattern A: any
// stuck-Charging order in production would have been written by the
// pre-D7 binary and the recon's job is to sweep it out, not retry a
// `Charge` that the post-D7 gateway no longer offers.
//
// GetStatus itself never blocks — it's effectively a constant. The
// ctx is honoured for symmetry with the port contract; a real adapter's
// HTTP call respects it.
func (g *MockGateway) GetStatus(ctx context.Context, _ uuid.UUID) (domain.ChargeStatus, error) {
	// Honour ctx even on a constant path so callers can rely on the
	// timeout boundary. Returning ChargeStatusUnknown + ctx.Err() is
	// the documented "transient infra failure" branch — the reconciler
	// treats it as "skip this order, retry next sweep".
	if err := ctx.Err(); err != nil {
		return domain.ChargeStatusUnknown, err
	}
	return domain.ChargeStatusNotFound, nil
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
// Why ctx is honoured even on a cheap (cache hit) path: a real adapter
// would call `POST /v1/payment_intents` over HTTP and respect
// ctx.Done(). Caller writes propagate the timeout boundary; we don't
// break it.
func (g *MockGateway) CreatePaymentIntent(ctx context.Context, orderID uuid.UUID, amountCents int64, currency string, metadata map[string]string) (domain.PaymentIntent, error) {
	if err := ctx.Err(); err != nil {
		return domain.PaymentIntent{}, err
	}

	// Idempotency short-circuit: same orderID → same intent. Stripe
	// behaviour. The amount/currency from this call are validated
	// against the cached intent so a buggy caller passing different
	// amounts on retry surfaces loudly rather than silently using the
	// first call's amount.
	//
	// Metadata is NOT validated against the cache — Stripe accepts
	// caller-controlled metadata edits on subsequent calls. A real
	// adapter passes the latest metadata through to the gateway via
	// `PaymentIntent.update`. Our mock just refreshes the cached value
	// so tests that exercise metadata-edit behaviour see the new
	// payload (and the eventual webhook emission picks it up).
	//
	// Race-safety on metadata edits: two concurrent CreatePaymentIntent
	// calls with different metadata would otherwise race on the
	// `Load(copy) → mutate copy → Store` sequence below (last writer
	// wins, but observers can see either value). Serialise via
	// `intentMetadataMu` so concurrent tests / callers see a consistent
	// post-update view. The first-time creation path still uses
	// LoadOrStore (it's already race-free).
	if cached, ok := g.intents.Load(orderID); ok {
		intent := cached.(domain.PaymentIntent)
		if intent.AmountCents != amountCents || intent.Currency != currency {
			return domain.PaymentIntent{}, errors.New("CreatePaymentIntent: cached intent has different amount/currency — caller passed inconsistent params on retry")
		}
		if metadata != nil {
			g.intentMetadataMu.Lock()
			// Re-load under the lock so concurrent updates serialise
			// against the latest cached struct rather than the snapshot
			// we read before acquiring the lock.
			cached, _ = g.intents.Load(orderID)
			intent = cached.(domain.PaymentIntent)
			intent.Metadata = metadata
			g.intents.Store(orderID, intent)
			g.intentMetadataMu.Unlock()
		}
		return intent, nil
	}

	// First-time creation. Mock latency so /pay p95 numbers in
	// benchmarks aren't unrealistically fast.
	latencyDelay := g.MaxLatency - g.MinLatency
	if latencyDelay <= 0 {
		latencyDelay = 1
	}
	latency := g.MinLatency + time.Duration(rand.Int64N(int64(latencyDelay))) //nolint:gosec // G404 — math/rand is correct for simulated latency in a mock
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		// Don't cache on cancellation — a retry with a non-cancelled
		// ctx still gets a fresh intent.
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
		Metadata:     metadata,
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

// Compile-time check that MockGateway implements both port halves AND
// the combined `PaymentGateway` (read+create). The assignment fails
// to type-check if any required method is missing. Pre-D7 this list
// also included `domain.PaymentCharger` (since deleted with the
// `Charge` method).
var (
	_ domain.PaymentStatusReader  = (*MockGateway)(nil)
	_ domain.PaymentIntentCreator = (*MockGateway)(nil)
	_ domain.PaymentGateway       = (*MockGateway)(nil)
)
