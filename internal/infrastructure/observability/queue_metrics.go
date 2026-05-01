package observability

import "booking_monitor/internal/application/worker"

type prometheusQueueMetrics struct{}

// NewQueueMetrics returns the Prometheus-backed QueueMetrics implementation.
func NewQueueMetrics() worker.QueueMetrics {
	return &prometheusQueueMetrics{}
}

func (m *prometheusQueueMetrics) RecordXAckFailure() {
	RedisXAckFailuresTotal.Inc()
}

func (m *prometheusQueueMetrics) RecordXAddFailure(stream string) {
	RedisXAddFailuresTotal.WithLabelValues(stream).Inc()
}

func (m *prometheusQueueMetrics) RecordRevertFailure() {
	RedisRevertFailuresTotal.Inc()
}

func (m *prometheusQueueMetrics) RecordDLQRoute(reason string) {
	RedisDLQRoutedTotal.WithLabelValues(reason).Inc()
}
