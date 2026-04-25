// Package application — DBMetrics port. Lives here (not in domain or
// infrastructure) so the impl in internal/infrastructure/observability/
// can depend on it without inverting the architectural direction
// (infrastructure → application → domain). The same pattern is used
// for WorkerMetrics, BookingMetrics, and QueueMetrics.
package application

// DBMetrics is the observability port for database-layer infrastructure
// failures that would otherwise be log-only. Surfaces rare but
// operationally-critical events (e.g. rollback itself failing) as a
// first-class metric so alerts can fire without scraping log text.
type DBMetrics interface {
	RecordRollbackFailure()
}

// noopDBMetrics satisfies DBMetrics for tests and unwired paths.
type noopDBMetrics struct{}

func (noopDBMetrics) RecordRollbackFailure() {}

// NoopDBMetrics returns a no-op DBMetrics implementation. Use in tests
// that don't assert on rollback-failure observability, instead of
// re-defining a private noop in every test package.
func NoopDBMetrics() DBMetrics { return noopDBMetrics{} }
