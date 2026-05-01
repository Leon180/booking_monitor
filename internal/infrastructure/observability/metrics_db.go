package observability

// DB-side infrastructure failure counter. The DBMetrics interface
// (port) lives in `internal/application/db_metrics.go`; the adapter
// that forwards to this var lives in this package's `db_metrics.go`.
// `db_pool_collector.go` is a separate concern (sql.DB.Stats() →
// gauges); it doesn't share var declarations with this file.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// DBRollbackFailuresTotal increments when tx.Rollback returns a
// non-sql.ErrTxDone error. ErrTxDone is expected after certain
// fatal errors and is filtered at the call site.
var DBRollbackFailuresTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "db_rollback_failures_total",
		Help: "Total number of transaction rollbacks that themselves failed (excluding sql.ErrTxDone)",
	},
)
