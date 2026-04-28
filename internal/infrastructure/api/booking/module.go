package booking

import "go.uber.org/fx"

// Module wires the booking handler + tracing decorator. Composed by
// the parent api.Module so consumers can either:
//
//   - import booking.Module directly when they need only the booking
//     surface (e.g., a future test harness), OR
//   - import api.Module to get every api/* sub-surface in one go
//     (the cmd/booking-cli/server.go pattern).
//
// fx.Decorate runs after fx.Provide, so NewBookingHandler resolves
// first and NewTracingBookingHandler wraps the result. Same shape
// as PR #41's instrumentedIdempotencyRepository decorator pattern.
var Module = fx.Module("api/booking",
	fx.Provide(NewBookingHandler),
	fx.Decorate(NewTracingBookingHandler),
)
