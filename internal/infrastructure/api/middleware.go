package api

import (
	"booking_monitor/internal/log"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CombinedMiddleware injects the base logger and the per-request
// correlation id into the context with exactly ONE context.WithValue
// and ONE c.Request.WithContext — no zap core clone.
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
func CombinedMiddleware(baseLogger *log.Logger) gin.HandlerFunc {
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
