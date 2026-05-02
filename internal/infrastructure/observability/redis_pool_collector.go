package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// RedisPoolCollector exposes go-redis client connection-pool statistics
// as Prometheus gauges + counters. Mirrors the DBPoolCollector pattern
// (same scrape-time read-snapshot model) and answers the diagnostic
// question that neither our app-side `cache_*` metrics nor the
// `redis_exporter` server-side `redis_*` metrics can answer:
//
//   "Is the Go redis client's connection pool itself the bottleneck?"
//
// Why this matters (the gap O3.1a alone leaves open): redis_exporter
// shows Redis CPU + per-command stats from the server's perspective.
// `cache_*` shows our app's hit/miss outcomes. NEITHER tells you that
// requests are queueing on the client side waiting for an idle pool
// connection — which is the most common saturation failure-mode for
// high-concurrency Go services that under-provisioned PoolSize.
//
// 2024-era industry research (silent-failure-hunter cited Stack Overflow
// running 87k cmd/s at 2% Redis CPU on 2 nodes) consistently flags
// connection-pool starvation as the answer to "Redis is slow" when the
// server-side metrics look idle. Without these gauges, the saturation
// profile in `make profile-saturation` cannot conclude "client-side
// pool is saturated" — it can only show that *something* is slow.
//
// Mapping to `redis.PoolStats` (which embeds `pool.Stats`):
//
//   .TotalConns      → redis_client_pool_total_conns        (gauge)
//   .IdleConns       → redis_client_pool_idle_conns         (gauge)
//   .StaleConns      → redis_client_pool_stale_conns        (gauge)
//   .Hits            → redis_client_pool_hits_total         (counter)
//   .Misses          → redis_client_pool_misses_total       (counter)
//   .Timeouts        → redis_client_pool_timeouts_total     (counter)  ← page-worthy
//   .WaitCount       → redis_client_pool_wait_count_total   (counter)
//   .WaitDurationNs  → redis_client_pool_wait_duration_seconds_total (counter)
//   .Unusable        → redis_client_pool_unusable_total     (counter)
//
// The counters are monotonic so Prometheus `rate()` over them gives
// pool-events/sec, which is what saturation-detection alerts care
// about. `Misses` rate sustained > 0 = pool too small (creating fresh
// connections instead of reusing); `Timeouts` rate > 0 = pool fully
// exhausted past PoolTimeout (a hard saturation signal).
//
// Naming convention: `redis_client_pool_*` distinguishes from:
//   - `redis_*`        — server-side, scraped from redis_exporter (job=redis)
//   - `redis_stream_*` — app-emitted scan results from StreamsCollector
//   - `pg_pool_*`      — sibling DBPoolCollector for Postgres
type RedisPoolCollector struct {
	client *redis.Client

	totalConns        *prometheus.Desc
	idleConns         *prometheus.Desc
	staleConns        *prometheus.Desc
	hits              *prometheus.Desc
	misses            *prometheus.Desc
	timeouts          *prometheus.Desc
	waitCount         *prometheus.Desc
	waitDurationTotal *prometheus.Desc
	unusable          *prometheus.Desc
}

// NewRedisPoolCollector wires the gauges + counters to a specific
// *redis.Client. Caller registers via the default registerer (typically
// fx.Invoke in `cache.Module`, mirroring registerStreamsCollector).
//
// Multiple Redis clients (read replica, separate cluster, etc.) need
// separate collectors with distinct label values — out of scope here,
// single-client today.
func NewRedisPoolCollector(client *redis.Client) *RedisPoolCollector {
	return &RedisPoolCollector{
		client: client,
		totalConns: prometheus.NewDesc(
			"redis_client_pool_total_conns",
			"Current total number of connections in the go-redis client pool (idle + in-use).",
			nil, nil,
		),
		idleConns: prometheus.NewDesc(
			"redis_client_pool_idle_conns",
			"Current number of idle connections in the go-redis client pool, reusable on next call.",
			nil, nil,
		),
		staleConns: prometheus.NewDesc(
			"redis_client_pool_stale_conns",
			"Current number of stale connections in the pool pending removal (e.g. exceeded ConnMaxIdleTime). Gauge, not counter — sourced from PoolStats.StaleConns which is a current-state field.",
			nil, nil,
		),
		hits: prometheus.NewDesc(
			"redis_client_pool_hits_total",
			"Cumulative number of times the pool returned a free connection (fast path).",
			nil, nil,
		),
		misses: prometheus.NewDesc(
			"redis_client_pool_misses_total",
			"Cumulative number of times the pool had to create a new connection because no idle one was available. Sustained rate > 0 → PoolSize is too small for the workload.",
			nil, nil,
		),
		timeouts: prometheus.NewDesc(
			"redis_client_pool_timeouts_total",
			"Cumulative number of times waiting for a pool connection exceeded PoolTimeout. Sustained rate > 0 → pool is fully exhausted; this is a hard saturation signal.",
			nil, nil,
		),
		waitCount: prometheus.NewDesc(
			"redis_client_pool_wait_count_total",
			"Cumulative number of times a goroutine had to wait for a connection (pool full, but not yet timed out).",
			nil, nil,
		),
		waitDurationTotal: prometheus.NewDesc(
			"redis_client_pool_wait_duration_seconds_total",
			"Cumulative time spent waiting for a connection from the pool, in seconds.",
			nil, nil,
		),
		unusable: prometheus.NewDesc(
			"redis_client_pool_unusable_total",
			"Cumulative number of times a connection was found to be unusable (e.g. broken from the server side) and discarded.",
			nil, nil,
		),
	}
}

// Describe implements prometheus.Collector. Sends one descriptor per
// metric the collector publishes.
func (c *RedisPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.totalConns
	ch <- c.idleConns
	ch <- c.staleConns
	ch <- c.hits
	ch <- c.misses
	ch <- c.timeouts
	ch <- c.waitCount
	ch <- c.waitDurationTotal
	ch <- c.unusable
}

// Collect implements prometheus.Collector. Snapshots `client.PoolStats()`
// at scrape time. PoolStats() is internally lock-protected and cheap
// (returns a struct copy via atomic loads), safe on every scrape.
func (c *RedisPoolCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.client.PoolStats()

	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(stats.TotalConns))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(stats.IdleConns))
	ch <- prometheus.MustNewConstMetric(c.staleConns, prometheus.GaugeValue, float64(stats.StaleConns))
	ch <- prometheus.MustNewConstMetric(c.hits, prometheus.CounterValue, float64(stats.Hits))
	ch <- prometheus.MustNewConstMetric(c.misses, prometheus.CounterValue, float64(stats.Misses))
	ch <- prometheus.MustNewConstMetric(c.timeouts, prometheus.CounterValue, float64(stats.Timeouts))
	ch <- prometheus.MustNewConstMetric(c.waitCount, prometheus.CounterValue, float64(stats.WaitCount))
	ch <- prometheus.MustNewConstMetric(c.waitDurationTotal, prometheus.CounterValue, float64(stats.WaitDurationNs)/1e9)
	ch <- prometheus.MustNewConstMetric(c.unusable, prometheus.CounterValue, float64(stats.Unusable))
}
