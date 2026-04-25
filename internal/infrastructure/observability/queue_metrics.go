package observability

import "booking_monitor/internal/application"

type prometheusQueueMetrics struct{}

// NewQueueMetrics returns the Prometheus-backed QueueMetrics implementation.
func NewQueueMetrics() application.QueueMetrics {
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
