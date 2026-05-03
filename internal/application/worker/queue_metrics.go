package worker

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

	// RecordDLQRoute increments per SUCCESSFUL message route to the
	// Redis DLQ, labelled by reason. Distinguishes:
	//   - "malformed_parse"      parseMessage failed (missing field, bad UUID)
	//   - "malformed_classified" handler returned a malformed-input error
	//                            (the DLQ fast-path that bypasses retry budget)
	//   - "exhausted_retries"    handler exceeded retry budget on transient errors
	// Counterpart to RecordXAddFailure(stream="dlq") which fires only
	// on failure — without RecordDLQRoute, operators have no positive
	// signal of DLQ throughput, only the absence of failures.
	RecordDLQRoute(reason string)

	// RecordConsumerGroupRecreated increments when the worker's
	// XReadGroup loop hits NOGROUP (consumer group was deleted, e.g.
	// by FLUSHALL or operator action) and self-heals by recreating it
	// via `XGROUP CREATE ... $`. The self-heal preserves availability
	// (the worker keeps consuming new messages) but **silently drops
	// any messages enqueued between the group's deletion and the
	// recreation moment** — `$` means "from current end of stream",
	// which is past those messages.
	//
	// In a healthy production system this counter MUST stay at 0.
	// Sustained > 0 means either (a) operations is FLUSHALL'ing the
	// production Redis (don't), or (b) something is deliberately
	// resetting the consumer group. Either way, the operator needs a
	// loud signal — see the `ConsumerGroupRecreated` alert in
	// `deploy/prometheus/alerts.yml`.
	//
	// Background: this is the underlying mechanism behind the 411-of-1000
	// silent message loss documented in `docs/architectural_backlog.md`
	// § "Cache-truth architecture". PR-A made the test-side trigger go
	// away (precise DEL instead of FLUSHALL); this metric (PR-C) makes
	// the production-side trigger LOUD instead of silent.
	RecordConsumerGroupRecreated()
}

// noopQueueMetrics satisfies QueueMetrics for tests and unwired paths.
type noopQueueMetrics struct{}

func (noopQueueMetrics) RecordXAckFailure()           {}
func (noopQueueMetrics) RecordXAddFailure(_ string)   {}
func (noopQueueMetrics) RecordRevertFailure()         {}
func (noopQueueMetrics) RecordDLQRoute(_ string)      {}
func (noopQueueMetrics) RecordConsumerGroupRecreated() {}

// NoopQueueMetrics returns a no-op QueueMetrics implementation. Use in
// tests that don't assert on queue-failure observability, instead of
// re-defining a private noop in every test package.
func NoopQueueMetrics() QueueMetrics { return noopQueueMetrics{} }
