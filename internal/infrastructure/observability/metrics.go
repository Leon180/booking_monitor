package observability

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// --- HTTP Metrics ---
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	// --- Business Metrics ---

	// BookingsTotal tracks booking outcomes at the API layer.
	// Labels: status = "success" | "sold_out" | "duplicate" | "error"
	BookingsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bookings_total",
			Help: "Total number of booking attempts by outcome",
		},
		[]string{"status"},
	)

	// WorkerOrdersTotal tracks order processing outcomes in the worker.
	// Labels: status = "success" | "sold_out" | "duplicate" | "db_error"
	WorkerOrdersTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_orders_total",
			Help: "Total number of orders processed by the worker, by outcome",
		},
		[]string{"status"},
	)

	// WorkerProcessingDuration tracks how long the worker takes to process each message.
	WorkerProcessingDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_processing_duration_seconds",
			Help:    "Duration of worker message processing in seconds",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		},
	)

	// InventoryConflictsTotal counts how often Redis approved but DB rejected (oversell prevention).
	InventoryConflictsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "inventory_conflicts_total",
			Help: "Total number of inventory conflicts (Redis approved, DB rejected)",
		},
	)
)

func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start).Seconds()
		status := c.Writer.Status()

		httpRequestsTotal.WithLabelValues(c.Request.Method, c.FullPath(), strconv.Itoa(status)).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, c.FullPath()).Observe(duration)
	}
}

// init pre-initializes all label combinations so they appear in /metrics from startup.
func init() {
	for _, status := range []string{"success", "sold_out", "duplicate", "error"} {
		BookingsTotal.WithLabelValues(status)
	}
	for _, status := range []string{"success", "sold_out", "duplicate", "db_error"} {
		WorkerOrdersTotal.WithLabelValues(status)
	}
}
