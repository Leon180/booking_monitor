package api

import (
	"booking_monitor/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func LoggerMiddleware(l *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Attach logger to context
		ctx := logger.WithCtx(c.Request.Context(), l)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
