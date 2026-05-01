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
