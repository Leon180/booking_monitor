package api

import (
	"booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CombinedMiddleware injects the request-scoped logger into the
// context in a single pass — one correlation id generated, one zap
// With to bake the id into a child logger, one context.WithValue,
// one c.Request.WithContext. Error-path log calls that retrieve the
// logger via log.FromContext(ctx) automatically emit the correlation
// field without any per-call With() clone.
//
// The correlation id is also echoed in the X-Correlation-ID response
// header and stored in gin.Context keys for middleware-level access.
//
// This replaces the previous LoggerMiddleware + CorrelationIDMiddleware
// pair, which did 2×WithValue + 2×WithContext (~464 bytes/req) on the
// hot path.
func CombinedMiddleware(baseLogger *log.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		corID := c.GetHeader("X-Correlation-ID")
		if corID == "" {
			corID = uuid.New().String()
		}
		c.Header("X-Correlation-ID", corID)

		reqLogger := baseLogger.With(tag.CorrelationID(corID))
		ctx := log.NewContext(c.Request.Context(), reqLogger)
		c.Request = c.Request.WithContext(ctx)
		c.Set("correlation_id", corID)

		c.Next()
	}
}
