package application

import (
	"context"

	"go.uber.org/fx"
)

var Module = fx.Module("application",
	fx.Provide(
		NewBookingService,
		NewEventService,
		NewWorkerService,
		NewOutboxRelay,
		NewSagaCompensator,
	),
	fx.Decorate(
		// BookingService chain: base -> tracing -> metrics
		func(svc BookingService) BookingService {
			return NewBookingServiceMetricsDecorator(
				NewBookingServiceTracingDecorator(svc),
			)
		},
		// WorkerService chain: base -> metrics
		func(svc WorkerService) WorkerService {
			return NewWorkerServiceMetricsDecorator(svc)
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
