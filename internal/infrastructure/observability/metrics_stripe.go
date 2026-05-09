package observability

// D4.2 Stripe SDK adapter metrics. Per-call observability for
// `internal/infrastructure/payment/stripe_gateway.go`'s outbound
// calls to api.stripe.com.
//
// Two metrics:
//
//   - stripe_api_calls_total{op,outcome}  Counter — one increment per
//     finished Stripe API call. `outcome` classifies the result via
//     the `domain.ErrPayment*` sentinel taxonomy (success / declined /
//     transient / misconfigured / invalid) so a single recording
//     rule + alert can gate on the failure-class ratio without
//     re-classifying error messages.
//
//   - stripe_api_duration_seconds{op}     Histogram — wall-clock cost
//     of one Stripe API call. Buckets target the 10ms..10.24s range
//     because Stripe's documented p99 is 89ms and timeouts are 30s
//     (clamped to 5min ceiling — see stripe_gateway.go). 10
//     exponential buckets give resolution at both the median and
//     the long-tail.
//
// `op` label values are the operation name (no Stripe-prefix, no
// /v1 path): `create_payment_intent`, `get_status`. Two values
// today; expanding when the adapter grows new Stripe operations
// (e.g. `cancel_payment_intent` if a future PR adds it).
//
// The application-side `payment.StripeMetrics` interface +
// `bootstrap.NewPrometheusStripeMetrics()` adapter forward into
// these vars; the StripeGateway adapter takes the interface (NOT
// these globals directly) so unit tests can inject a fake
// (`NopStripeMetrics` or a test spy) without polluting the
// process-global Prometheus registry.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// StripeAPICallsTotal counts Stripe API calls by operation + outcome.
// Outcome label maps to the domain.ErrPayment* sentinel taxonomy:
//
//   - "success"        — call succeeded (no error)
//   - "declined"       — ErrPaymentDeclined (card-side error; 422 to client)
//   - "transient"      — ErrPaymentTransient (429 / 5xx / network — retry-eligible)
//   - "misconfigured"  — ErrPaymentMisconfigured (401 / 403 — page-worthy)
//   - "invalid"        — ErrPaymentInvalid (400 / idempotency — non-retryable)
//
// Pre-warmed at boot via `metrics_init.go` so PromQL alerts can
// evaluate `rate(stripe_api_calls_total{op="create_payment_intent",
// outcome="misconfigured"}[5m])` from second 0, not from "first time
// we observed misconfigured".
var StripeAPICallsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "stripe_api_calls_total",
		Help: "Stripe API calls by operation and outcome (success/declined/transient/misconfigured/invalid)",
	},
	[]string{"op", "outcome"},
)

// StripeAPIDurationSeconds records the wall-clock cost of one Stripe
// API call (excludes stripe-go's internal retry budget — each retry
// attempt is a separate observation if the SDK loops, so the
// histogram reflects per-attempt latency, not aggregate budget).
//
// Bucket choice: exponential 10ms..81.92s (14 buckets) covers the
// expected p50 (~30-50ms localhost-equivalent), p99 (~89ms per
// Stripe's published SLO), AND long-tail timeouts (30s default,
// 5min adapter ceiling).
//
// Slice 2b review LOW #1 fix: original 10 buckets capped at 5.12s
// — so any 30s timeout fell into +Inf only, making timeout-class
// p99 calculations blind. Extended to 14 buckets so the 30s
// default timeout sits between 32.77s (bucket 12) and 65.54s
// (bucket 13), giving real resolution in the timeout tail.
//
// Same bucket shape works for both PaymentIntent.Create and
// PaymentIntent.Retrieve — Stripe's API performance is roughly
// uniform across these read/write ops.
var StripeAPIDurationSeconds = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "stripe_api_duration_seconds",
		Help:    "Stripe API call duration (per-attempt; stripe-go retries observed as separate samples)",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 14), // 10ms, 20ms, 40ms, ..., 40.96s, 81.92s
	},
	[]string{"op"},
)
