package application

// DBMetrics is the observability port for database-layer infrastructure
// failures that would otherwise be log-only. Surfaces rare but
// operationally-critical events (e.g. rollback itself failing) as a
// first-class metric so alerts can fire without scraping log text.
type DBMetrics interface {
	RecordRollbackFailure()
}
