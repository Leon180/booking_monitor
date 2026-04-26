package postgres

import (
	"go.uber.org/fx"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/observability"
)

// Module exports the Postgres infrastructure providers and decorators.
//
// Each repository is provided twice:
//   1. As the concrete `*postgresXRepository` (so PostgresUnitOfWork in
//      this same package can call `WithTx` on it directly).
//   2. Re-exposed under its `domain.XRepository` port via a tiny adapter
//      so application-layer consumers depend only on the interface.
// `fx.Decorate` then wraps the interface entry with tracing — the
// concrete entry is unaffected, which is desired: tx-bound repos used
// inside `UnitOfWork.Do` skip per-method spans (the surrounding caller-
// level span captures the tx as a unit), while non-tx read paths keep
// the per-method spans.
//
// DBMetrics impl is provided here (not separately in commonFxOptions)
// so the metrics provider always travels with the consumer
// (PostgresUnitOfWork) — including postgres.Module without the matching
// observability provider would otherwise fail at fx startup, not at
// compile time. Symmetric with cache.Module's QueueMetrics provision.
var Module = fx.Module("postgres",
	fx.Provide(
		// Concrete repos — used by PostgresUnitOfWork.
		NewPostgresEventRepository,
		NewPostgresOrderRepository,
		NewPostgresOutboxRepository,
		// Domain-port adapters — used by application services.
		func(r *postgresEventRepository) domain.EventRepository { return r },
		func(r *postgresOrderRepository) domain.OrderRepository { return r },
		func(r *postgresOutboxRepository) domain.OutboxRepository { return r },

		NewPostgresUnitOfWork,
		NewPostgresDistributedLock,
		observability.NewDBMetrics,
	),
	fx.Decorate(
		NewEventRepositoryTracingDecorator,
		NewOrderRepositoryTracingDecorator,
	),
)
