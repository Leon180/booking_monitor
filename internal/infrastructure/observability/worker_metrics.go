package observability

import (
	"time"

	"booking_monitor/internal/application/worker"
)

// prometheusWorkerMetrics implements worker.Metrics using Prometheus counters.
type prometheusWorkerMetrics struct{}

func NewWorkerMetrics() worker.Metrics {
	return &prometheusWorkerMetrics{}
}

func (m *prometheusWorkerMetrics) RecordOrderOutcome(status string) {
	WorkerOrdersTotal.WithLabelValues(status).Inc()
}

func (m *prometheusWorkerMetrics) RecordProcessingDuration(d time.Duration) {
	WorkerProcessingDuration.Observe(d.Seconds())
}

func (m *prometheusWorkerMetrics) RecordInventoryConflict() {
	InventoryConflictsTotal.Inc()
}
