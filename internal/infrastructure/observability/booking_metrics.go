package observability

import "booking_monitor/internal/application"

// prometheusBookingMetrics implements application.BookingMetrics by
// incrementing the BookingsTotal counter declared in metrics.go.
type prometheusBookingMetrics struct{}

// NewBookingMetrics returns the Prometheus-backed implementation.
// Wired into fx in cmd/booking-cli/main.go.
func NewBookingMetrics() application.BookingMetrics {
	return &prometheusBookingMetrics{}
}

func (m *prometheusBookingMetrics) RecordBookingOutcome(status string) {
	BookingsTotal.WithLabelValues(status).Inc()
}
