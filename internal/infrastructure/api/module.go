package api

import "go.uber.org/fx"

var Module = fx.Module("api",
	fx.Provide(
		NewBookingHandler,
		NewHealthHandler,
	),
	fx.Decorate(
		NewTracingBookingHandler,
	),
)
