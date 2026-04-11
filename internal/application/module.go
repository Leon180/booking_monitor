package application

import (
	"context"

	"go.uber.org/fx"
	"go.uber.org/zap"

	"booking_monitor/internal/domain"
)

var Module = fx.Module("application",
	// BookingService is provided as a fully-decorated chain:
	//   base -> tracing -> metrics
	// We use fx.Provide with an inline constructor rather than
	// fx.Provide(NewBookingService) + fx.Decorate, because fx.Decorate
	// is module-scoped from fx v1.17+ — it only applies to consumers
	// inside the same fx.Module. Our api.Module consumes BookingService
	// from outside, so it would receive the base (undecorated)
	// instance and skip every metric / trace. Inlining the wrap at
	// the provide site sidesteps the scoping issue entirely.
	fx.Provide(
		func(
			eventRepo domain.EventRepository,
			orderRepo domain.OrderRepository,
			inventoryRepo domain.InventoryRepository,
			uow domain.UnitOfWork,
		) BookingService {
			base := NewBookingService(eventRepo, orderRepo, inventoryRepo, uow)
			return NewBookingServiceMetricsDecorator(
				NewBookingServiceTracingDecorator(base),
			)
		},
		NewEventService,
		NewOutboxRelay,
		NewSagaCompensator,
	),
	// WorkerService is provided similarly — wrap with metrics at the
	// provide site so any future external consumer gets the decorated
	// instance.
	fx.Provide(
		func(
			queue domain.OrderQueue,
			orderRepo domain.OrderRepository,
			eventRepo domain.EventRepository,
			outboxRepo domain.OutboxRepository,
			uow domain.UnitOfWork,
			metrics domain.WorkerMetrics,
			logger *zap.SugaredLogger,
		) WorkerService {
			base := NewWorkerService(queue, orderRepo, eventRepo, outboxRepo, uow, metrics, logger)
			return NewWorkerServiceMetricsDecorator(base)
		},
	),
	// Start the outbox relay (with tracing) as a background goroutine managed by the Fx lifecycle.
	//
	// The run-context is derived from context.Background() rather than a
	// caller-supplied ctx because the relay must survive any individual
	// fx lifecycle hook timeout. OnStop invokes cancel() explicitly so
	// the background is still bounded by the fx lifecycle — just
	// decoupled from the OnStop ctx deadline.
	fx.Invoke(func(lc fx.Lifecycle, relay *OutboxRelay) {
		traced := NewOutboxRelayTracingDecorator(relay)
		ctx, cancel := context.WithCancel(context.Background())
		lc.Append(fx.Hook{
			OnStart: func(_ context.Context) error {
				go traced.Run(ctx)
				return nil
			},
			OnStop: func(_ context.Context) error {
				cancel() // Signal relay to stop; Run() will return on next tick
				return nil
			},
		})
	}),
)
