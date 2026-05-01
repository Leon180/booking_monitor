package worker

import "time"

// Metrics is the observability port consumed by the worker
// service. It lives in application (not domain) because metrics are a
// cross-cutting technical concern — they're not part of the ubiquitous
// language, don't participate in any domain invariant, and the domain
// entities remain fully correct without them. The implementation
// (Prometheus counters / histograms) lives in infrastructure.
type Metrics interface {
	RecordOrderOutcome(status string)
	RecordProcessingDuration(d time.Duration)
	RecordInventoryConflict()
}
