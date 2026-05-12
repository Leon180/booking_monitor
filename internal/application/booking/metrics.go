// Package booking is the booking-flow application service —
// the API entry point for `POST /api/v1/book` and `GET /orders/:id`.
// Promoted to its own subpackage in CP2.6 (was 5 flat files at
// `internal/application/`) so the booking flow can grow without
// further crowding the top-level namespace, and so the Pattern A
// reservation refactor (Phase 3) extends a coherent home rather
// than adding to the flat list.
//
// Naming convention follows the existing payment / recon / saga
// subpackages: types drop the redundant `Booking` prefix because the
// package qualifier already says it (`booking.Service` not
// `booking.BookingService`, `booking.Metrics` not
// `booking.BookingMetrics`).
package booking

// Metrics is the observability port consumed by the booking-service
// metrics decorator. The Prometheus-backed implementation lives in
// `internal/infrastructure/observability`; this interface keeps the
// application layer independent of that singleton.
type Metrics interface {
	// RecordBookingOutcome records the terminal outcome of a BookTicket
	// call. Status must be one of "success", "sold_out", "error".
	RecordBookingOutcome(status string)
}

// Stage5Metrics is the Stage 5-specific observability port. Stage 5's
// hot path has additional failure modes (Kafka publish failure with
// or without successful revert) that the base Metrics interface
// doesn't model. Defined here (not in observability) so the
// kafkaIntakeService can depend on the interface without importing
// the prometheus-backed singleton — same pattern as Metrics above.
//
// A no-op implementation (Stage5NopMetrics) lives in this package so
// tests can construct the service without wiring Prometheus.
type Stage5Metrics interface {
	// RecordPublishFailure increments the publish-failure counter.
	// `reverted` is true when the inventory was successfully rolled
	// back after the publish failed (clean compensation); false when
	// the revert ALSO failed (drift — inventory permanently leaked
	// until the drift reconciler / manual ops intervenes).
	RecordPublishFailure(reverted bool)
}

// Stage5NopMetrics is the test / dev no-op implementation of
// Stage5Metrics. The Prometheus-backed implementation lives in
// `internal/infrastructure/observability`.
type Stage5NopMetrics struct{}

// RecordPublishFailure is a no-op.
func (Stage5NopMetrics) RecordPublishFailure(bool) {}
