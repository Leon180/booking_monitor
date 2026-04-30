package bootstrap

import (
	"booking_monitor/internal/application/recon"
	"booking_monitor/internal/application/saga"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
)

// This file contains the boundary translation between the
// infrastructure layer (yaml/env-tagged configs + global Prometheus
// vars) and the application-layer Config/Metrics types defined in
// `internal/application/{recon,saga}`. Living in `bootstrap`
// preserves the dependency direction: bootstrap may import both
// `application` and `infrastructure`; neither leaf imports the
// other (closes Phase 2 checkpoint A1 / row 10).
//
// Each section has two adapters:
//
//   1. Translator: cfg.* → application Config{}. Plain copy + Validate.
//      Validation fires at startup (NewX returns error); fx surfaces it
//      as a fatal so the process refuses to run with a misconfigured
//      Config rather than silently producing wrong behaviour.
//
//   2. Metrics adapter: a struct that implements the application's
//      Metrics interface by forwarding each method to the corresponding
//      global prometheus.Counter / Histogram / Gauge in
//      infrastructure/observability. Stateless — safe to provide as a
//      single-value via fx.Provide.

// ── recon ────────────────────────────────────────────────────────────

// NewReconConfig translates the wire-format ReconConfig into the
// application-layer recon.Config and validates it. Used by
// `cmd/booking-cli/recon.go` via fx.Provide.
func NewReconConfig(cfg *config.Config) (recon.Config, error) {
	c := recon.Config{
		SweepInterval:     cfg.Recon.SweepInterval,
		ChargingThreshold: cfg.Recon.ChargingThreshold,
		GatewayTimeout:    cfg.Recon.GatewayTimeout,
		MaxChargingAge:    cfg.Recon.MaxChargingAge,
		BatchSize:         cfg.Recon.BatchSize,
	}
	if err := c.Validate(); err != nil {
		return recon.Config{}, err
	}
	return c, nil
}

// prometheusReconMetrics implements recon.Metrics by forwarding each
// emission to the corresponding singleton in
// `internal/infrastructure/observability`. Stateless — methods are
// pure pass-through; the value receiver makes copies cheap and the
// type satisfies the interface.
type prometheusReconMetrics struct{}

// NewPrometheusReconMetrics is the fx-friendly constructor. Returning
// `recon.Metrics` rather than the concrete type keeps the fx graph
// honest: callers can substitute another adapter (e.g. a fake in
// integration tests) without touching wiring.
func NewPrometheusReconMetrics() recon.Metrics { return prometheusReconMetrics{} }

func (prometheusReconMetrics) SetStuckChargingOrders(c int) {
	observability.ReconStuckChargingOrders.Set(float64(c))
}
func (prometheusReconMetrics) IncFindStuckErrors() {
	observability.ReconFindStuckErrorsTotal.Inc()
}
func (prometheusReconMetrics) IncGatewayErrors() {
	observability.ReconGatewayErrorsTotal.Inc()
}
func (prometheusReconMetrics) IncResolved(outcome string) {
	observability.ReconResolvedTotal.WithLabelValues(outcome).Inc()
}
func (prometheusReconMetrics) IncMarkErrors() {
	observability.ReconMarkErrorsTotal.Inc()
}
func (prometheusReconMetrics) ObserveResolveDuration(seconds float64) {
	observability.ReconResolveDurationSeconds.Observe(seconds)
}
func (prometheusReconMetrics) ObserveResolveAge(seconds float64) {
	observability.ReconResolveAgeSeconds.Observe(seconds)
}
func (prometheusReconMetrics) ObserveGatewayDuration(seconds float64) {
	observability.ReconGatewayDurationSeconds.Observe(seconds)
}

// ── saga watchdog ────────────────────────────────────────────────────

// NewSagaConfig translates the wire-format SagaConfig into the
// application-layer saga.Config and validates it. Used by
// `cmd/booking-cli/saga_watchdog.go` via fx.Provide.
func NewSagaConfig(cfg *config.Config) (saga.Config, error) {
	c := saga.Config{
		WatchdogInterval: cfg.Saga.WatchdogInterval,
		StuckThreshold:   cfg.Saga.StuckThreshold,
		MaxFailedAge:     cfg.Saga.MaxFailedAge,
		BatchSize:        cfg.Saga.BatchSize,
	}
	if err := c.Validate(); err != nil {
		return saga.Config{}, err
	}
	return c, nil
}

// prometheusSagaMetrics implements saga.Metrics. Same shape /
// rationale as prometheusReconMetrics above.
type prometheusSagaMetrics struct{}

// NewPrometheusSagaMetrics is the fx-friendly constructor.
func NewPrometheusSagaMetrics() saga.Metrics { return prometheusSagaMetrics{} }

func (prometheusSagaMetrics) SetStuckFailedOrders(c int) {
	observability.SagaStuckFailedOrders.Set(float64(c))
}
func (prometheusSagaMetrics) IncResolved(outcome string) {
	observability.SagaWatchdogResolvedTotal.WithLabelValues(outcome).Inc()
}
func (prometheusSagaMetrics) IncFindStuckErrors() {
	observability.SagaWatchdogFindStuckErrorsTotal.Inc()
}
func (prometheusSagaMetrics) ObserveResolveDuration(seconds float64) {
	observability.SagaWatchdogResolveDurationSeconds.Observe(seconds)
}
func (prometheusSagaMetrics) ObserveResolveAge(seconds float64) {
	observability.SagaWatchdogResolveAgeSeconds.Observe(seconds)
}
