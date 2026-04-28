package ops

import "go.uber.org/fx"

// Module wires the health handler. Composed by the parent api.Module.
//
// Why a separate fx.Module for ops despite only providing one type
// today: future operational endpoints (N3 SLO admin endpoints, N9
// auth-gated debug routes) get added here without inflating the
// booking-side surface, and the named "api/ops" node in fx graphs
// makes the customer/operator boundary visible.
var Module = fx.Module("api/ops",
	fx.Provide(NewHealthHandler),
)
