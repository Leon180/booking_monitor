package observability

import "booking_monitor/internal/application"

type prometheusDBMetrics struct{}

// NewDBMetrics returns the Prometheus-backed DBMetrics implementation.
func NewDBMetrics() application.DBMetrics {
	return &prometheusDBMetrics{}
}

func (m *prometheusDBMetrics) RecordRollbackFailure() {
	DBRollbackFailuresTotal.Inc()
}
