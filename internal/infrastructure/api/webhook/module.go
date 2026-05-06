package webhook

import (
	"fmt"
	"time"

	"go.uber.org/fx"

	"booking_monitor/internal/application/payment"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
)

// Module wires the D5 webhook surface — verifier-backed handler that
// fronts the application-side WebhookService. Composed by the parent
// api.Module so cmd/booking-cli/server.go can pick up the entire
// HTTP boundary in one go.
//
// fx.Provide chain:
//
//	cfg + svc + metrics + logger
//	  → newHandlerFromConfig (asserts non-empty webhook secret)
//	  → *Handler
//
// The application-side WebhookService is provided by
// `internal/application/payment` (a separate fx module).
var Module = fx.Module("api/webhook",
	fx.Provide(newHandlerFromConfig),
)

// newHandlerFromConfig is the fx-side constructor that pulls webhook
// dependencies (secret, tolerance) from cfg and asserts the secret is
// non-empty so the application aborts startup rather than running
// with a no-op verifier.
//
// Why fail at construction time, not at first request: a webhook
// listener with an empty secret silently accepts every signature
// (the HMAC of an empty key is well-defined and forgeable). That's
// a forgery vector — surface it when the operator can still fix
// the env var.
func newHandlerFromConfig(
	cfg *config.Config,
	svc payment.WebhookService,
	metrics HandlerMetrics,
	logger *mlog.Logger,
) (*Handler, error) {
	if cfg.Payment.WebhookSecret == "" {
		return nil, fmt.Errorf("webhook: PAYMENT_WEBHOOK_SECRET is empty (refusing to start with a forgeable verifier)")
	}
	tol := cfg.Payment.WebhookReplayTolerance
	if tol <= 0 {
		tol = 5 * time.Minute
	}
	// payment.WebhookService directly satisfies webhook.PaymentService
	// (identical signature) — no adapter needed.
	return NewHandler(svc, []byte(cfg.Payment.WebhookSecret), tol, metrics, logger), nil
}
