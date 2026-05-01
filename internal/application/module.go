package application

import (
	"go.uber.org/fx"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/application/event"
	"booking_monitor/internal/domain"
)

// Module wires the cross-package application graph: booking decorators
// (which need fx.Provide for the chain composition) + standalone
// services that don't yet have their own subpackage (EventService,
// SagaCompensator).
//
// CP2.6b note: worker + outbox fx wirings deliberately live in
// `cmd/booking-cli/server.go` (not here) because their subpackages
// import `application` (for `UnitOfWork`, `Repositories`,
// `NewOrderCreatedEvent`, `EventPublisher`, `DistributedLock`), so an
// `application → {worker,outbox}` edge here would create an import
// cycle. This matches the pattern already used by payment/saga/recon:
// each cmd file owns its own fx.Provide for its subpackage's
// constructors. booking/ is the exception — it doesn't import
// application, so application can safely import it for the
// decorator-chain wiring.
var Module = fx.Module("application",
	// booking.Service is provided as a fully-decorated chain:
	//   base -> tracing -> metrics
	// We use fx.Provide with an inline constructor rather than
	// fx.Provide(booking.NewService) + fx.Decorate, because fx.Decorate
	// is module-scoped from fx v1.17+ — it only applies to consumers
	// inside the same fx.Module. Our api.Module consumes booking.Service
	// from outside, so it would receive the base (undecorated)
	// instance and skip every metric / trace. Inlining the wrap at
	// the provide site sidesteps the scoping issue entirely.
	fx.Provide(
		func(
			orderRepo domain.OrderRepository,
			inventoryRepo domain.InventoryRepository,
			metrics booking.Metrics,
		) booking.Service {
			base := booking.NewService(orderRepo, inventoryRepo)
			return booking.NewMetricsDecorator(
				booking.NewTracingDecorator(base),
				metrics,
			)
		},
		event.NewService,
	),
)
