package observability

import "booking_monitor/internal/application/booking"

// prometheusBookingMetrics implements `booking.Metrics` by
// incrementing the BookingsTotal counter declared in metrics.go.
type prometheusBookingMetrics struct{}

// NewBookingMetrics returns the Prometheus-backed implementation.
// Wired into fx in cmd/booking-cli/main.go.
func NewBookingMetrics() booking.Metrics {
	return &prometheusBookingMetrics{}
}

func (m *prometheusBookingMetrics) RecordBookingOutcome(status string) {
	BookingsTotal.WithLabelValues(status).Inc()
}
