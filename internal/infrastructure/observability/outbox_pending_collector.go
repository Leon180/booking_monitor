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
//	SELECT COUNT(id) FROM events_outbox WHERE processed_at IS NULL
//
// Migration 000007 added a partial index on
// `events_outbox(id) WHERE processed_at IS NULL`, so this is an
// index-only scan: cheap even on large tables (sub-millisecond at
// 100k rows; bounded by the partial index size, NOT the full table).
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
// OutboxPendingCollectorErrorsTotal and emits NO gauge — the absence
// of the metric is itself a signal that pairs with the errors counter
// for alerting.
func (c *OutboxPendingCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	var count int64
	err := c.db.QueryRowContext(ctx,
		`SELECT COUNT(id) FROM events_outbox WHERE processed_at IS NULL`,
	).Scan(&count)
	if err != nil {
		OutboxPendingCollectorErrorsTotal.Inc()
		return
	}
	ch <- prometheus.MustNewConstMetric(c.pendingDesc, prometheus.GaugeValue, float64(count))
}
