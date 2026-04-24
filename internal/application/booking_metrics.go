package application

// BookingMetrics is the observability port consumed by the booking
// service metrics decorator. Lives in application — parallel to
// WorkerMetrics — so the decorator stops reaching into
// infrastructure/observability directly. The Prometheus implementation
// lives in infrastructure.
type BookingMetrics interface {
	// RecordBookingOutcome records the terminal outcome of a BookTicket
	// call. Status must be one of "success", "sold_out", "error".
	RecordBookingOutcome(status string)
}
