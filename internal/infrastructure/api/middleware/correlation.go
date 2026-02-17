package middleware

import (
	"booking_monitor/pkg/logger"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const HeaderXCorrelationID = "X-Correlation-ID"

// CorrelationIDMiddleware checks for X-Correlation-ID header or generates a new one.
// It also enriches the context logger with the correlation_id field.
func CorrelationIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		corID := c.GetHeader(HeaderXCorrelationID)
		if corID == "" {
			corID = uuid.New().String()
		}

		// Set header for response
		c.Header(HeaderXCorrelationID, corID)

		// 1. Get existing logger from context (or global)
		l := logger.FromCtx(c.Request.Context())

		// 2. Add correlation_id field
		l = l.With("correlation_id", corID)

		// 3. Update context with new logger
		ctx := logger.WithCtx(c.Request.Context(), l)
		c.Request = c.Request.WithContext(ctx)

		// 4. Also Set in Gin Keys for easy access if needed
		c.Set("correlation_id", corID)

		c.Next()
	}
}
