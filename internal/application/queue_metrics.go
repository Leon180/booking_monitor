package application

// QueueMetrics is the observability port for Redis-queue infrastructure
// failures that would otherwise be log-only. Each method records a
// specific class of failure the worker / compensation path can
// experience; operators need counters to alert on spikes without
// regexing logs.
//
// Lives in application (not domain or infrastructure) so the impl in
// internal/infrastructure/observability/ can depend on it without
// inverting the architectural direction. See db_metrics.go and
// worker_metrics.go for the same pattern.
type QueueMetrics interface {
	// RecordXAckFailure increments when Redis XAck returns an error —
	// message will be re-delivered via PEL, so the counter is the only
	// signal that a message was double-processed.
	RecordXAckFailure()

	// RecordXAddFailure increments when an XAdd to the given stream
	// fails. stream is the target stream name (e.g. "dlq") so the
	// metric can distinguish main-stream vs DLQ write failures.
	RecordXAddFailure(stream string)

	// RecordRevertFailure increments when the inventory compensation
	// (RevertInventory) fails during handleFailure. A non-zero rate
	// here means Redis inventory is drifting from DB state.
	RecordRevertFailure()
}

// noopQueueMetrics satisfies QueueMetrics for tests and unwired paths.
type noopQueueMetrics struct{}

func (noopQueueMetrics) RecordXAckFailure()         {}
func (noopQueueMetrics) RecordXAddFailure(_ string) {}
func (noopQueueMetrics) RecordRevertFailure()       {}

// NoopQueueMetrics returns a no-op QueueMetrics implementation. Use in
// tests that don't assert on queue-failure observability, instead of
// re-defining a private noop in every test package.
func NoopQueueMetrics() QueueMetrics { return noopQueueMetrics{} }
