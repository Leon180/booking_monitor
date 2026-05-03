package observability

import (
	"context"
	"database/sql"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// OutboxPendingCollector exposes the count of unprocessed transactional-
// outbox rows as a Prometheus gauge. Implements prometheus.Collector
// directly so the count is taken at scrape time (not on a polling
// timer) — fresher data, no goroutine, no race against fx lifecycle.
//
// Why this exists. The transactional outbox is the bridge between
// "DB committed an order.created / order.failed event" and "Kafka
// downstream consumers see it". If the OutboxRelay is healthy, this
// gauge sits at ~ in-flight events (small, drains within seconds).
// If the relay breaks (crashed leader, advisory lock contention,
// poll loop hung, broker unreachable), pending grows unbounded and
// the saga compensator / payment service / any downstream consumer
// goes silent. Without this gauge, the only signal is downstream
// effects (saga not compensating, customer 202s never advancing),
// which arrive minutes-to-hours late.
//
// The companion alert `OutboxPendingBacklog` fires when the gauge
// stays elevated, distinguishing "relay is keeping up but slow" from
// "relay is wedged". Closes the gap that the search-specialist
// review of the cache-truth architecture (PR-D follow-up) identified
// as #3 in scenario assessment — outbox failure was traceable only
// via downstream effects.
//
// Mapping to the underlying query:
//
//	SELECT COUNT(*) FROM events_outbox WHERE processed_at IS NULL
//
// Migration 000007 added a partial index on
// `events_outbox(id) WHERE processed_at IS NULL`, so this is an
// index-only scan: cheap even on large tables (sub-millisecond at
// 100k rows; bounded by the partial index size, NOT the full table).
// COUNT(*) is the canonical Postgres idiom — guaranteed to use the
// partial index regardless of statistics state. COUNT(id) would
// require a NULL-check on the column per SQL semantics; the planner
// often elides this for PK columns but the contract is not stable
// across PG versions.
//
// On query failure (DB outage, missing migration, query timeout) the
// collector increments a dedicated errors counter and emits NO
// pending-count metric. Same discipline as StreamsCollector — partial
// metrics beat failing the whole /metrics scrape, but operators must
// be able to distinguish "backlog is zero" from "we cannot tell".
type OutboxPendingCollector struct {
	db      *sql.DB
	timeout time.Duration

	pendingDesc *prometheus.Desc
}

// outboxPendingScrapeBudget bounds the per-scrape COUNT query. Same
// rationale as StreamsCollector's 1s timeout: prevent a stuck DB from
// pinning the scrape thread. Well below Prometheus's default 15s
// scrape interval so the bound is safe.
const outboxPendingScrapeBudget = 1 * time.Second

// NewOutboxPendingCollector wires the gauge to a specific *sql.DB.
// Caller registers the returned collector with the default registerer
// (typically via fx in bootstrap.CommonModule).
func NewOutboxPendingCollector(db *sql.DB) *OutboxPendingCollector {
	return &OutboxPendingCollector{
		db:      db,
		timeout: outboxPendingScrapeBudget,
		pendingDesc: prometheus.NewDesc(
			"outbox_pending_count",
			"Current number of transactional-outbox rows with processed_at IS NULL (events awaiting Kafka publish).",
			nil, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *OutboxPendingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.pendingDesc
}

// Collect implements prometheus.Collector. Runs ONE COUNT query per
// scrape under a tight ctx timeout. On query failure, bumps
// OutboxPendingCollectorErrorsTotal AND emits the gauge as 0.
//
// Why emit 0 (not omit) on error. Prometheus's TSDB retains the
// last-known value of a series for `staleness_delta` (default 5
// minutes) after a scrape stops emitting it. If we silently omitted
// the gauge during a DB outage, the previous scrape's value (say
// 200, well above the 100 threshold) would persist for those 5
// minutes and `OutboxPendingBacklog > 100 for 5m` would continue
// firing on stale data — exactly when the operator most needs the
// alert to mean "the outbox is wedged" rather than "the gauge is
// dead". By emitting 0, the backlog alert clears within a single
// scrape and the operator's only signal during DB outage is the
// companion `OutboxPendingCollectorDown` alert (paired with the
// errors counter), which carries the unambiguous "data is unreliable"
// meaning. The trade-off — a brief DB blip during a real backlog
// would clear and re-arm the backlog alert, costing the 5m soak
// window — is acceptable because real backlogs persist much longer
// than blips.
func (c *OutboxPendingCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	var count int64
	err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events_outbox WHERE processed_at IS NULL`,
	).Scan(&count)
	if err != nil {
		OutboxPendingCollectorErrorsTotal.Inc()
		ch <- prometheus.MustNewConstMetric(c.pendingDesc, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.pendingDesc, prometheus.GaugeValue, float64(count))
}
