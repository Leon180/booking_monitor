package api

import (
	"booking_monitor/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// LoggerMiddleware injects the base logger into the context.
// DEPRECATED: prefer CombinedMiddleware which consolidates logger +
// correlation ID into a single context.WithValue call.
func LoggerMiddleware(l *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := logger.WithCtx(c.Request.Context(), l)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// CombinedMiddleware merges LoggerMiddleware + CorrelationIDMiddleware
// into a single middleware that does exactly ONE context.WithValue and
// ONE c.Request.WithContext per request.
//
// Why: each context.WithValue allocates a 32-byte valueCtx struct, and
// each WithContext clones the entire *http.Request (~200 bytes). The
// old two-middleware chain did 2+2 of these; this does 1+1, saving
// ~232 bytes/request × 17k RPS = ~3.9 MB/s of heap allocation.
func CombinedMiddleware(baseLogger *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		corID := c.GetHeader("X-Correlation-ID")
		if corID == "" {
			corID = uuid.New().String()
		}
		c.Header("X-Correlation-ID", corID)

		rctx := &logger.RequestContext{
			Logger:        baseLogger,
			CorrelationID: corID,
		}
		ctx := logger.WithRequestContext(c.Request.Context(), rctx)
		c.Request = c.Request.WithContext(ctx)
		c.Set("correlation_id", corID)

		c.Next()
	}
}
