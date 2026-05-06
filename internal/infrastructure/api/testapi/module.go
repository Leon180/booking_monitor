package testapi

import (
	"fmt"

	"go.uber.org/fx"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
)

// Module wires the test-only payment-confirm handler. The handler
// itself is harmless (404s to unknown order ids); the actual GATE is
// the conditional `RegisterRoutes` call inside
// `cmd/booking-cli/server.go::buildGinEngine` — when
// `cfg.Server.EnableTestEndpoints` is false the route group never
// mounts and the handler is unreachable.
//
// We still construct the *Handler unconditionally because fx Module
// composition is static — making the constructor itself conditional
// would require a separate "test build" / `fx.Module` per environment,
// which is heavier than just trusting the route gate.
var Module = fx.Module("api/testapi",
	fx.Provide(newHandlerFromConfig),
)

func newHandlerFromConfig(
	orderRepo domain.OrderRepository,
	cfg *config.Config,
	logger *mlog.Logger,
) (*Handler, error) {
	if cfg.Payment.WebhookSecret == "" {
		// Same fail-fast posture as the real webhook handler: the
		// test endpoint signs envelopes with this secret, so an
		// empty secret means we'd produce signatures the verifier
		// rejects. Surface as a startup error rather than a confusing
		// runtime 401 chain.
		return nil, fmt.Errorf("testapi: PAYMENT_WEBHOOK_SECRET is empty (test endpoint cannot sign envelopes)")
	}
	target := cfg.Payment.WebhookLoopbackURL
	if target == "" {
		target = "http://127.0.0.1:" + cfg.Server.Port + "/webhook/payment"
	}
	return NewHandler(orderRepo, []byte(cfg.Payment.WebhookSecret), target, logger), nil
}
