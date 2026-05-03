package observability

// Transactional-outbox observability surface. The OutboxRelay
// (internal/application/outbox/relay.go) does NOT directly use this
// package — the metric below is exposed by the OutboxPendingCollector
// at scrape time, NOT by the relay's own loop. This keeps the relay's
// hot path free of metric writes (it's already advisory-locked +
// per-cycle-bounded; adding gauge writes would just be ceremony).

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// OutboxPendingCollectorErrorsTotal increments when the per-scrape
// `SELECT COUNT(id) FROM events_outbox WHERE processed_at IS NULL`
// query fails. Distinct signal from `outbox_pending_count` going to
// zero — the latter could mean "backlog cleared", but if this counter
// is climbing the gauge is silent because we cannot count, NOT because
// the backlog is small.
//
// Mirrors the RedisStreamCollectorErrorsTotal pattern (PR-D / cache-
// truth roadmap follow-up): partial /metrics output beats failing the
// whole scrape, but the operator must be able to distinguish "backlog
// is healthy" from "we cannot tell". The companion alert
// `OutboxPendingCollectorDown` fires on sustained > 0 rate.
var OutboxPendingCollectorErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "outbox_pending_collector_errors_total",
		Help: "Total scrape-time COUNT query failures for events_outbox WHERE processed_at IS NULL (DB outage, missing migration, query timeout — outbox_pending_count gauge is unreliable while this is climbing)",
	},
)
