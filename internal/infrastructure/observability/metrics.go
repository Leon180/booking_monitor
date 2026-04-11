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
			Name: "http_request_duration_seconds",
			Help: "Duration of HTTP requests in seconds",
			// Finer buckets for accurate p99 calculation (5ms to 2.5s)
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		},
		[]string{"method", "path"},
	)

	// --- Advanced Metrics (Phase 7.7) ---

	// PageViewsTotal tracks users entering the page for conversion rate calculation.
	PageViewsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "page_views_total",
			Help: "Total number of page views to measure funnel conversion",
		},
		[]string{"page"},
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

	// DLQMessagesTotal counts messages routed to a dead-letter queue by
	// topic and reason. Labels:
	//   topic  = "order.created.dlq" | "order.failed.dlq"
	//   reason = "invalid_payload" | "invalid_event" | "max_retries"
	DLQMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dlq_messages_total",
			Help: "Total number of messages written to a dead-letter queue",
		},
		[]string{"topic", "reason"},
	)

	// SagaPoisonMessagesTotal counts saga events that exceeded the
	// compensator retry budget and were dead-lettered.
	SagaPoisonMessagesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "saga_poison_messages_total",
			Help: "Total number of saga events dead-lettered after max retries",
		},
	)

	// KafkaConsumerRetryTotal counts Kafka messages that failed to
	// process transiently and were left UNCOMMITTED for Kafka rebalance
	// to re-deliver. This is the "silent retry" surface: if this
	// counter's rate stays high, it means downstream infra (DB / Redis
	// / payment gateway) is degraded and consumers are in a stuck-but-
	// not-dead state. Labels:
	//   topic  = original Kafka topic name
	//   reason = "transient_processing_error" for now; future budget
	//            implementation will add "retry_budget_exceeded" etc.
	//
	// Paired with the `KafkaConsumerStuck` Prometheus alert which
	// fires when `rate(kafka_consumer_retry_total[5m]) > 1` for 2m.
	KafkaConsumerRetryTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kafka_consumer_retry_total",
			Help: "Total Kafka messages left uncommitted for rebalance-based retry due to transient errors",
		},
		[]string{"topic", "reason"},
	)
)

func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Writer.Status())

		httpRequestsTotal.WithLabelValues(c.Request.Method, c.FullPath(), status).Inc()
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
	for _, topic := range []string{"order.created.dlq", "order.failed.dlq"} {
		for _, reason := range []string{"invalid_payload", "invalid_event", "max_retries"} {
			DLQMessagesTotal.WithLabelValues(topic, reason)
		}
	}
	for _, topic := range []string{"order.created", "order.failed"} {
		KafkaConsumerRetryTotal.WithLabelValues(topic, "transient_processing_error")
	}
}
