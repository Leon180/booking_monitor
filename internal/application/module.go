package application

import "go.uber.org/fx"

var Module = fx.Module("application",
	fx.Provide(
		NewBookingService,
		NewEventService,
		NewWorkerService,
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
)
