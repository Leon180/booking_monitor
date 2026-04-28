// Package middleware holds Gin handlers that apply cross-cutting
// behaviour to every HTTP request — currently logger + correlation-id
// injection. Future arrivals: auth/RBAC (PR N9), rate-limit
// enforcement, request body size capping.
//
// Lives separately from api/booking and api/ops because middleware
// runs ahead of route dispatch — neither subpackage owns it. Keeping
// it here makes "what fires on every request, regardless of route"
// findable in one place.
//
// fx wiring note: this package intentionally has no fx.Module. Gin
// middleware takes a *log.Logger directly and gets called once at
// engine build time (cmd/booking-cli/server.go::buildGinEngine), so
// pulling it through fx.Provide → fx.Invoke would be ceremony with no
// payoff — the logger is already in scope at the call site. New
// middleware added here MUST also be wired in buildGinEngine; there
// is no fx error that will surface a forgotten r.Use(...) call. If
// the count of middlewares grows past ~5, revisit this and consider
// a per-middleware fx.Provide so missing wiring shows up at boot.
package middleware

import (
	"booking_monitor/internal/log"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Combined injects the base logger and the per-request correlation
// id into the context with exactly ONE context.WithValue and ONE
// c.Request.WithContext — no zap core clone.
//
// How the correlation id reaches log lines:
//
//   - It is stored in ctx as a plain string (not baked into the
//     logger via With).
//   - When a caller invokes log.Error(ctx, ...) — either the package
//     function or *Logger method — enrichFields reads the string
//     from ctx and prepends it to the fields slice of that single
//     log emission.
//
// The result: happy-path requests pay ~40 bytes (valueCtx +
// ctxValue struct), not ~1.2 KB (valueCtx + cloned zap core).
// Requests that never log (the 99% hot path) pay even less — the
// enrichFields fast path returns the user slice unchanged.
//
// Trace ids: if OTEL middleware (or the service-level tracing
// decorator) has put a SpanContext in ctx by the time a log fires,
// enrichFields also emits trace_id and span_id from that span. No
// configuration needed here.
//
// Renamed from `CombinedMiddleware` (when it lived in `package api`)
// to avoid the stutter `middleware.CombinedMiddleware` at the call
// site. `middleware.Combined(logger)` reads naturally and matches
// the Go-standard "drop the package suffix on exported names"
// convention (e.g., `bytes.Buffer`, not `bytes.BytesBuffer`).
func Combined(baseLogger *log.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		corID := c.GetHeader("X-Correlation-ID")
		if corID == "" {
			corID = uuid.New().String()
		}
		c.Header("X-Correlation-ID", corID)

		// Single WithValue: stash base logger + correlation id as a
		// small struct value. No zap clone.
		ctx := log.NewContext(c.Request.Context(), baseLogger, corID)
		c.Request = c.Request.WithContext(ctx)
		c.Set("correlation_id", corID)

		c.Next()
	}
}
