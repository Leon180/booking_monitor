package postgres

import (
	"go.uber.org/fx"

	"booking_monitor/internal/infrastructure/observability"
)

// Module exports the Postgres infrastructure providers and decorators.
// DBMetrics impl is provided here (not separately in commonFxOptions)
// so the metrics provider always travels with the consumer
// (PostgresUnitOfWork) — including postgres.Module without the matching
// observability provider would otherwise fail at fx startup, not at
// compile time. Symmetric with cache.Module's QueueMetrics provision.
var Module = fx.Module("postgres",
	fx.Provide(
		// Provide basic repositories
		NewPostgresEventRepository,
		NewPostgresOrderRepository,
		NewPostgresOutboxRepository,
		NewPostgresUnitOfWork,
		NewPostgresDistributedLock,
		observability.NewDBMetrics,
	),
	// Apply Decorators
	fx.Decorate(
		NewEventRepositoryTracingDecorator,
		NewOrderRepositoryTracingDecorator,
	),
)
