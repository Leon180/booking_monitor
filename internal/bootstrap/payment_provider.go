package bootstrap

// payment_provider.go wires the gateway adapter selected by
// `Payment.Provider` config (D4.2). Shared between cmd/booking-cli
// subcommands (server.go + recon.go) so both fx graphs select the
// same concrete adapter without duplicating the switch logic.
//
// fx integration pattern: the subcommand's fx.Provide list calls
// `bootstrap.NewPaymentGateway` (returns `domain.PaymentGateway`,
// the combined port) and projects to the narrow port it actually
// needs via `fx.As`:
//
//   server.go (needs CreatePaymentIntent for /pay):
//     fx.Annotate(bootstrap.NewPaymentGateway,
//                 fx.As(new(domain.PaymentIntentCreator)))
//
//   recon.go (needs GetStatus for stuck-charging probe):
//     fx.Annotate(bootstrap.NewPaymentGateway,
//                 fx.As(new(domain.PaymentStatusReader)))
//
// Both adapters (`MockGateway` + `StripeGateway`) implement the
// combined `domain.PaymentGateway`, so projection to either narrow
// port works identically.

import (
	"fmt"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	paymentInfra "booking_monitor/internal/infrastructure/payment"
	mlog "booking_monitor/internal/log"
)

// prometheusStripeMetrics implements `payment.StripeMetrics` by
// forwarding each method call into the Prometheus globals declared
// in `internal/infrastructure/observability/metrics_stripe.go`.
// Mirrors the `prometheusReconMetrics` / `prometheusCompensatorMetrics`
// patterns — keeps the application-side adapter free of direct
// Prometheus imports while letting the production wiring do the real
// emission.
type prometheusStripeMetrics struct{}

// NewPrometheusStripeMetrics returns the Prometheus-backed
// StripeMetrics implementation. fx wires it as the production
// adapter; tests substitute `NopStripeMetrics` or a spy.
func NewPrometheusStripeMetrics() paymentInfra.StripeMetrics {
	return prometheusStripeMetrics{}
}

func (prometheusStripeMetrics) IncCall(op, outcome string) {
	observability.StripeAPICallsTotal.WithLabelValues(op, outcome).Inc()
}

func (prometheusStripeMetrics) ObserveDuration(op string, seconds float64) {
	observability.StripeAPIDurationSeconds.WithLabelValues(op).Observe(seconds)
}

// NewPaymentGateway returns the configured payment gateway adapter.
// Returns an error (NOT panic) on:
//   - unknown PAYMENT_PROVIDER value (caught here as defense-in-depth;
//     `config.Validate` already whitelists the value at startup)
//   - StripeGateway construction failure (missing API key, bad
//     timeout config, etc.)
//
// Returning error rather than panicking lets the fx graph surface
// the failure as a clean startup error via `fx.Shutdowner` (the
// subcommand's main exits non-zero with a clear stderr message),
// matching the existing pattern for other infra-config failures.
//
// `domain.PaymentGateway` is the combined port (PaymentIntentCreator
// + PaymentStatusReader). Each subcommand's fx graph projects to
// the narrow port it needs via `fx.As` — see file-level comment.
func NewPaymentGateway(
	cfg *config.Config,
	logger *mlog.Logger,
	metrics paymentInfra.StripeMetrics,
) (domain.PaymentGateway, error) {
	switch cfg.Payment.Provider {
	case "stripe":
		sg, err := paymentInfra.NewStripeGateway(paymentInfra.StripeConfig{
			APIKey:            cfg.Payment.Stripe.APIKey,
			Timeout:           cfg.Payment.Stripe.Timeout,
			MaxNetworkRetries: cfg.Payment.Stripe.MaxNetworkRetries,
		}, logger, metrics)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: stripe adapter init: %w", err)
		}
		return sg, nil
	case "mock", "":
		// Empty-string treated as "mock" — Validate() already accepts
		// this case (env-default is "mock"; literal-empty is for
		// `&Config{...}` test constructions that bypass cleanenv).
		// MockGateway doesn't take StripeMetrics — never emits to the
		// counter (mock-only deploys see the pre-warmed-zero series).
		return paymentInfra.NewMockGateway(), nil
	default:
		// Defense-in-depth — `config.Validate` already whitelists the
		// provider value, but if a future code path bypasses Validate
		// (a `&Config{...}` test literal that sets Provider directly,
		// for instance), this surfaces the typo rather than silently
		// defaulting to mock.
		return nil, fmt.Errorf("bootstrap: unknown PAYMENT_PROVIDER=%q (expected 'mock' or 'stripe')", cfg.Payment.Provider)
	}
}
