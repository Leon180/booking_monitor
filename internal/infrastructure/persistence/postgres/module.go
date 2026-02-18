package postgres

import (
	"go.uber.org/fx"
)

// Module exports the Postgres infrastructure providers and decorators
var Module = fx.Module("postgres",
	fx.Provide(
		// Provide basic repositories
		NewPostgresEventRepository,
		NewPostgresOrderRepository,
		NewPostgresOutboxRepository,
		NewPostgresUnitOfWork,
	),
	// Apply Decorators
	fx.Decorate(
		NewEventRepositoryTracingDecorator,
		NewOrderRepositoryTracingDecorator,
	),
)
