package application

import (
	"context"

	"go.uber.org/fx"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
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
			orderRepo domain.OrderRepository,
			inventoryRepo domain.InventoryRepository,
			metrics BookingMetrics,
		) BookingService {
			base := NewBookingService(orderRepo, inventoryRepo)
			return NewBookingServiceMetricsDecorator(
				NewBookingServiceTracingDecorator(base),
				metrics,
			)
		},
		NewEventService,
		NewOutboxRelay,
		NewSagaCompensator,
	),
	// WorkerService is provided as:
	//   base MessageProcessor -> metrics decorator -> WorkerService
	//
	// Future tracing decorator (see BookingServiceTracingDecorator for
	// the pattern) MUST sit between metrics and WorkerService:
	//   base -> metrics -> tracing -> WorkerService
	// so that tracing spans wrap the metrics work too. Reversing the
	// order would either double-count metrics inside spans or hide
	// span timing inside the metrics histogram.
	//
	// The processor chain is wrapped once here so every code path that
	// resolves a WorkerService gets the decorated chain. Keeping the
	// composition inline (not a free-floating fx.Provide of
	// MessageProcessor) avoids publishing the base processor into the
	// fx graph where a future consumer could accidentally depend on
	// the undecorated instance and skip metrics.
	fx.Provide(
		func(
			queue domain.OrderQueue,
			uow UnitOfWork,
			metrics WorkerMetrics,
			logger *mlog.Logger,
		) WorkerService {
			base := NewOrderMessageProcessor(uow, logger)
			processor := NewMessageProcessorMetricsDecorator(base, metrics)
			return NewWorkerService(queue, processor, logger)
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
