package domain

import "time"

// WorkerMetrics defines the metrics interface for the worker service.
// Keeping this in domain breaks the import cycle between application and observability.
type WorkerMetrics interface {
	RecordOrderOutcome(status string)
	RecordProcessingDuration(d time.Duration)
	RecordInventoryConflict()
}
