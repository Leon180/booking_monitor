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
