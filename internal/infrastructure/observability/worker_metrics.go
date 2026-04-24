package observability

import (
	"time"

	"booking_monitor/internal/application"
)

// prometheusWorkerMetrics implements application.WorkerMetrics using Prometheus counters.
type prometheusWorkerMetrics struct{}

func NewWorkerMetrics() application.WorkerMetrics {
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
