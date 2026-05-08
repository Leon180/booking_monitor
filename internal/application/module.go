package application

import (
	"go.uber.org/fx"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
)

// Module wires the cross-package application graph for subpackages
// that do NOT import the application package themselves (booking is
// the only one in this category — it doesn't need UnitOfWork or
// Repositories because BookTicket only touches Redis).
//
// CP2.6b + D4.1 note: worker / outbox / event fx wirings deliberately
// live in `cmd/booking-cli/server.go` (not here) because their
// subpackages import `application` (for `UnitOfWork`, `Repositories`,
// `EventPublisher`, `DistributedLock`), so an
// `application → {worker,outbox,event}` edge here would create an
// import cycle. This matches the pattern already used by
// payment/saga/recon: each cmd file owns its own fx.Provide for its
// subpackage's constructors.
//
// event/ moved out of this module in D4.1 (this commit) — pre-D4.1
// it was a standalone service that didn't need the UoW; now it
// orchestrates a multi-aggregate transaction (event + ticket_type),
// which means importing application for the UoW interface, which
// closes the cycle. Same root cause as CP2.6b's worker/outbox move.
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
			ticketTypeRepo domain.TicketTypeRepository,
			inventoryRepo domain.InventoryRepository,
			cfg *config.Config,
			metrics booking.Metrics,
		) booking.Service {
			base := booking.NewService(orderRepo, ticketTypeRepo, inventoryRepo, cfg)
			return booking.NewMetricsDecorator(
				booking.NewTracingDecorator(base),
				metrics,
			)
		},
	),
)
