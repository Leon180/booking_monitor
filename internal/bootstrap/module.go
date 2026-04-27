package bootstrap

import (
	"go.uber.org/fx"

	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	postgresRepo "booking_monitor/internal/infrastructure/persistence/postgres"
)

// CommonModule is the fx options every booking-cli subcommand needs:
//
//   - Logger (LogModule) — ctx-aware *log.Logger built from cfg
//   - Config provider — exposes the loaded *config.Config for injection
//   - DB pool — *sql.DB with retry-until-reachable semantics
//   - Postgres repos — postgresRepo.Module providing the repo interfaces
//   - Base observability — extended Go runtime metrics + DB pool gauges
//     registered into prometheus.DefaultRegisterer
//
// What "common" means here: anything a Go process boot needs before
// it can begin domain-specific work. Adding things only one subcommand
// uses (Kafka publisher, Gin engine, payment gateway) is OUT of scope —
// those wire alongside CommonModule in the subcommand-specific fx.New
// call.
//
// Both runServer and runPaymentWorker call this with the same cfg so
// both processes register the same base collectors. The payment
// worker has no /metrics listener today, but the registrations are
// harmless (no scrape, no overhead) and will start emitting
// automatically when N7 (k8s manifests) adds a metrics endpoint to
// the worker for cluster-side scraping.
func CommonModule(cfg *config.Config) fx.Option {
	return fx.Options(
		LogModule,
		fx.Provide(func() *config.Config { return cfg }),
		fx.Provide(provideDB),
		postgresRepo.Module,
		fx.Invoke(observability.RegisterRuntimeMetrics),
		fx.Invoke(registerDBPoolCollector),
	)
}
