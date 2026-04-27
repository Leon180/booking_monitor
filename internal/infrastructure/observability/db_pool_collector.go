package observability

import (
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"
)

// DBPoolCollector exposes Postgres `*sql.DB` pool statistics as
// Prometheus gauges. Implements `prometheus.Collector` directly so the
// readings are taken at scrape time (not on a polling timer) — fresher
// data, no goroutine, no race against fx lifecycle.
//
// Why we need it: `*sql.DB` connection pool is the most common
// saturation point under load before CPU/memory show pressure. Without
// these gauges, "Postgres slow" looks identical to "we ran out of pool
// connections and queued for 2s before timing out." The 4 gauges below
// are the canonical USE-method (Brendan Gregg) signals for the pool.
//
// Mapping to `sql.DBStats`:
//
//   .OpenConnections      → pg_pool_open_connections (total = inuse + idle)
//   .InUse                → pg_pool_in_use
//   .Idle                 → pg_pool_idle
//   .WaitCount            → pg_pool_wait_count_total (counter, monotonic)
//   .WaitDuration         → pg_pool_wait_duration_seconds_total (counter)
//   .MaxIdleClosed        → pg_pool_max_idle_closed_total (counter)
//   .MaxLifetimeClosed    → pg_pool_max_lifetime_closed_total (counter)
//
// The Wait* and *Closed counters are monotonic by design — Prometheus's
// `rate()` over them gives the closes/sec or wait-events/sec, which is
// what alerts care about ("more than N seconds spent waiting per
// minute"). `OpenConnections` etc. are gauges (current state).
type DBPoolCollector struct {
	db *sql.DB

	openConnections   *prometheus.Desc
	inUse             *prometheus.Desc
	idle              *prometheus.Desc
	waitCount         *prometheus.Desc
	waitDuration      *prometheus.Desc
	maxIdleClosed     *prometheus.Desc
	maxLifetimeClosed *prometheus.Desc
}

// NewDBPoolCollector wires the gauges to a specific `*sql.DB`. The
// caller registers the returned collector with the default registerer
// (typically via fx in cmd/booking-cli/main.go). Multiple `*sql.DB`
// instances (read replica, etc.) need separate collectors with distinct
// `db` label values — out of scope here, single-pool today.
func NewDBPoolCollector(db *sql.DB) *DBPoolCollector {
	return &DBPoolCollector{
		db: db,
		openConnections: prometheus.NewDesc(
			"pg_pool_open_connections",
			"Current number of established Postgres connections (in-use + idle).",
			nil, nil,
		),
		inUse: prometheus.NewDesc(
			"pg_pool_in_use",
			"Current number of Postgres connections actively serving a query.",
			nil, nil,
		),
		idle: prometheus.NewDesc(
			"pg_pool_idle",
			"Current number of Postgres connections sitting idle in the pool.",
			nil, nil,
		),
		waitCount: prometheus.NewDesc(
			"pg_pool_wait_count_total",
			"Cumulative number of times a goroutine had to wait for a Postgres connection.",
			nil, nil,
		),
		waitDuration: prometheus.NewDesc(
			"pg_pool_wait_duration_seconds_total",
			"Cumulative time spent waiting for a Postgres connection, in seconds.",
			nil, nil,
		),
		maxIdleClosed: prometheus.NewDesc(
			"pg_pool_max_idle_closed_total",
			"Cumulative number of connections closed because SetMaxIdleConns was exceeded.",
			nil, nil,
		),
		maxLifetimeClosed: prometheus.NewDesc(
			"pg_pool_max_lifetime_closed_total",
			"Cumulative number of connections closed because SetConnMaxLifetime was exceeded.",
			nil, nil,
		),
	}
}

// Describe implements `prometheus.Collector`. Sends one descriptor per
// gauge / counter the collector publishes.
func (c *DBPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.openConnections
	ch <- c.inUse
	ch <- c.idle
	ch <- c.waitCount
	ch <- c.waitDuration
	ch <- c.maxIdleClosed
	ch <- c.maxLifetimeClosed
}

// Collect implements `prometheus.Collector`. Snapshots `db.Stats()` at
// scrape time and emits one metric per descriptor. `db.Stats()` is
// internally lock-protected and cheap (returns a struct copy) so it's
// safe to call on every scrape.
func (c *DBPoolCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.db.Stats()

	ch <- prometheus.MustNewConstMetric(c.openConnections, prometheus.GaugeValue, float64(stats.OpenConnections))
	ch <- prometheus.MustNewConstMetric(c.inUse, prometheus.GaugeValue, float64(stats.InUse))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(stats.Idle))
	ch <- prometheus.MustNewConstMetric(c.waitCount, prometheus.CounterValue, float64(stats.WaitCount))
	ch <- prometheus.MustNewConstMetric(c.waitDuration, prometheus.CounterValue, stats.WaitDuration.Seconds())
	ch <- prometheus.MustNewConstMetric(c.maxIdleClosed, prometheus.CounterValue, float64(stats.MaxIdleClosed))
	ch <- prometheus.MustNewConstMetric(c.maxLifetimeClosed, prometheus.CounterValue, float64(stats.MaxLifetimeClosed))
}
