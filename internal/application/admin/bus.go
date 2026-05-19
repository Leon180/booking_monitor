package admin

// Bus is the publishing port for admin events. Implementations must
// satisfy two contracts:
//
//  1. **Non-blocking**: Publish must return promptly regardless of
//     downstream state. The publisher is on the business hot path
//     (worker / webhook / saga / sweeper) and cannot be delayed by
//     observability concerns. See design doc § Q3.
//
//  2. **Safe for concurrent use**: callers from multiple goroutines
//     can invoke Publish simultaneously without coordination.
//
// Events may be dropped silently when the implementation's internal
// buffer is at capacity. Drops MUST be reported via the
// `admin_event_bus_dropped_total` metric so operators can observe
// sustained backpressure. Per design § Q5/§Q16, no caller-visible
// error is surfaced — PG audit tables are the source of truth for
// individual records; this bus is best-effort for the live SSE tail.
//
// The concrete implementation lives in
// `internal/infrastructure/cache/admin_event_bus.go` (Redis Streams
// backed via the bounded-async drainer pattern).
type Bus interface {
	Publish(evt AdminEvent)
}

// noopBus is a do-nothing Bus suitable for tests or for fx wiring
// when admin event publishing is intentionally disabled (e.g., in
// the recon / saga-watchdog sidecar binaries that don't need to
// stream admin events).
type noopBus struct{}

// NewNoopBus returns a Bus that discards every event.
func NewNoopBus() Bus { return noopBus{} }

// Publish silently discards the event.
func (noopBus) Publish(_ AdminEvent) {}
