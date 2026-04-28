// Package api is the umbrella for the HTTP boundary. The actual
// handlers live in subpackages — see the Module below for the
// composition tree.
//
// Subpackage layout:
//
//	api/booking/    customer-facing endpoints (POST /book, GET /history,
//	                POST /events, GET /events/:id) + tracing decorator
//	                + error→HTTP translator. Mounted under /api/v1.
//	api/ops/        k8s probe endpoints (/livez, /readyz). Mounted at
//	                the engine root, NOT under /api/v1, because
//	                operational contracts must not move with API
//	                versioning.
//	api/middleware/ Gin handlers that apply to every request: logger +
//	                correlation-id injection. Future home for auth /
//	                rate-limit middleware (N9).
//	api/dto/        Request/response DTOs shared across the booking
//	                handlers. JSON tags + validation rules live here so
//	                domain types stay JSON-unaware.
//
// The split was introduced after PR #43's main.go cleanup so that
// growing the operational and middleware surfaces (planned for N3,
// N7, N9) doesn't turn the api/ root into a junk drawer.
package api

import (
	"go.uber.org/fx"

	"booking_monitor/internal/infrastructure/api/booking"
	"booking_monitor/internal/infrastructure/api/ops"
)

// Module composes the subpackage modules so consumers (cmd/booking-cli/
// server.go) wire one module and get the entire HTTP boundary.
//
// Order is irrelevant — fx resolves Provides via the dependency graph
// and Decorates after Provides. The two named children
// (api/booking, api/ops) appear in fx error logs and graph dumps.
var Module = fx.Module("api",
	booking.Module,
	ops.Module,
)
