package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// httpRequestsTotal + httpRequestDuration live here (HTTP-layer
// middleware) instead of `internal/infrastructure/observability/`
// because they're emitted by the Gin pipeline and serve only this
// middleware. Keeping them here removes the gin → observability
// import edge that previously coupled the metrics layer to a specific
// HTTP framework. Phase 2 checkpoint A2 (CP3 row 11) — closes the
// "metrics.go is a 593-line god-file mixing HTTP middleware with
// worker/recon/saga counters" finding.
//
// The metric NAMES are an external operator contract (alerts, Grafana
// dashboards, runbook queries reference them by exact name). Keep
// them stable across this relocation.
var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "Duration of HTTP requests in seconds",
			// Finer buckets for accurate p99 calculation (5ms to 2.5s)
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		},
		[]string{"method", "path"},
	)
)

// Metrics is the per-request RED-method observability middleware.
// Records request count + duration with method/path/status labels.
//
// Lives in api/middleware/ (not observability/) because it depends on
// the gin engine; the metric vars above are colocated for the same
// reason. Wired in cmd/booking-cli/server.go via r.Use(middleware.Metrics()).
//
// CP3 history note: previously named MetricsMiddleware and lived at
// observability.MetricsMiddleware. The rename drops the redundant
// "Middleware" suffix because the package qualifier already says it.
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Writer.Status())

		httpRequestsTotal.WithLabelValues(c.Request.Method, c.FullPath(), status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, c.FullPath()).Observe(duration)
	}
}
