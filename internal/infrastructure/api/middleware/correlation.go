package middleware

import (
	"booking_monitor/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const HeaderXCorrelationID = "X-Correlation-ID"

// CorrelationIDMiddleware checks for X-Correlation-ID header or generates
// a new one, then stores it in the context as a plain string.
//
// IMPORTANT: This middleware does NOT call l.With("correlation_id", ...)
// anymore. The old version cloned the entire zap core on every request
// (~1.2 KB heap allocation), which at 8k RPS accounted for 25% of total
// GC pressure (4.1 GB/min). Now the correlation ID is stored as a
// lightweight context.Value string. Callers that need it in their logs
// should use:
//
//	logger.FromCtx(ctx).With("correlation_id", logger.CorrelationIDFromCtx(ctx))
//
// The hot path (BookTicket success) never logs, so this has zero impact
// on happy-path observability. Error paths that do log should add the
// field explicitly.
func CorrelationIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		corID := c.GetHeader(HeaderXCorrelationID)
		if corID == "" {
			corID = uuid.New().String()
		}

		// Set header for response
		c.Header(HeaderXCorrelationID, corID)

		// Store as lightweight context value — no logger clone.
		ctx := logger.WithCorrelationID(c.Request.Context(), corID)
		c.Request = c.Request.WithContext(ctx)

		// Also set in Gin Keys for easy access if needed.
		c.Set("correlation_id", corID)

		c.Next()
	}
}
