package application

// QueueMetrics is the observability port for Redis-queue infrastructure
// failures that would otherwise be log-only. Each method records a
// specific class of failure the worker / compensation path can
// experience; operators need counters to alert on spikes without
// regexing logs.
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
