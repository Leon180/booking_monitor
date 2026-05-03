package observability

// Redis-stream / DLQ infrastructure failure counters. These surface
// log-only infra failures (XAck failures, XAdd failures, failed
// compensation, stream-collector scrape failures, DLQ routes) as
// first-class signals. Rate > 0 on any of them is an operational
// red flag; corresponding log lines remain for post-mortem detail.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RedisXAckFailuresTotal increments on XAck failure — message is
// retained in PEL and will be re-delivered, so this counter is the
// only leading signal that double-processing may have occurred.
var RedisXAckFailuresTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "redis_xack_failures_total",
		Help: "Total number of Redis XAck failures (message stays in PEL and will be re-delivered)",
	},
)

// RedisXAddFailuresTotal increments on XAdd failure, labelled by
// target stream. Currently only DLQ writes use XAdd from Go; label
// is kept so future main-stream writers can share this counter.
var RedisXAddFailuresTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "redis_xadd_failures_total",
		Help: "Total number of Redis XAdd failures by target stream",
	},
	[]string{"stream"},
)

// RedisRevertFailuresTotal increments when handleFailure's
// RevertInventory call fails — the message stays in PEL, so the
// counter lets operators alert on compensation drift before it
// shows up as Redis/DB inventory disagreement.
var RedisRevertFailuresTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "redis_revert_failures_total",
		Help: "Total number of RevertInventory failures during worker compensation (message retained in PEL)",
	},
)

// RedisStreamCollectorErrorsTotal increments when the
// StreamsCollector's Redis calls (XLEN / XPENDING) fail during
// a Prometheus scrape. Without this counter, a sustained Redis
// outage causes the stream-length / pending / lag metric series
// to silently disappear from /metrics — Prometheus's own
// scrape-staleness handling only catches *whole-scrape* failures
// (HTTP 5xx/timeout), NOT selective metric omission within a
// successful scrape.
//
// The collector deliberately does NOT propagate per-call errors
// up the scrape (emitting partial metrics is preferable to
// failing the whole /metrics endpoint), but that means
// dashboards alone can't distinguish "stream is empty" from
// "Redis is down". This counter closes that gap.
//
// Pairs with the alert `RedisStreamCollectorDown`
// (rate > 0 for 2m → critical) which fires when the collector
// itself is degraded.
var RedisStreamCollectorErrorsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "redis_stream_collector_errors_total",
		Help: "Streams observability collector Redis-call failures (XLEN/XPENDING) — sustained > 0 = collector degraded, stream-* gauges may be stale",
	},
	[]string{"stream", "operation"},
)

// RedisDLQRoutedTotal counts SUCCESSFUL routes to the Redis DLQ
// (orders:dlq), labelled by reason so operators can distinguish
// malformed-parse failures from malformed-classification failures
// from exhausted-retry failures. Counterpart to
// RedisXAddFailuresTotal(stream="dlq") — that one only fires on
// failure; this one fires on success. Together they let alerts
// trigger on either spike (malformed flood) or absence (DLQ
// throughput drops to zero, suggesting upstream is silent).
var RedisDLQRoutedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "redis_dlq_routed_total",
		Help: "Total number of messages successfully routed to orders:dlq, labelled by reason",
	},
	[]string{"reason"},
)

// ConsumerGroupRecreatedTotal counts NOGROUP self-heal events — every
// time the worker's XReadGroup returned NOGROUP and we ran
// `XGROUP CREATE ... $` to recover. The recovery preserves availability
// (worker keeps consuming new messages) at the cost of silently dropping
// any messages that were enqueued in the window between the group's
// disappearance and the recreation moment.
//
// In a healthy production system this counter MUST stay at 0. Any
// non-zero rate is an architectural failure signal — the most likely
// causes are:
//   - operator ran FLUSHALL on the production Redis (don't)
//   - operator deleted the consumer group via XGROUP DESTROY (don't)
//   - Redis crashed and AOF wasn't enabled (PR-B intentionally turned
//     off AOF; the plan is "rehydrate inventory from DB on restart" —
//     but in-flight stream messages have no DB analog and will be lost)
//
// See `docs/architectural_backlog.md` § "Cache-truth architecture" for
// the full reasoning. Paired with the `ConsumerGroupRecreated` alert
// in `deploy/prometheus/alerts.yml` so the operator gets paged
// instead of seeing silent data loss.
var ConsumerGroupRecreatedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "consumer_group_recreated_total",
		Help: "Total number of NOGROUP self-heal events on the orders:stream consumer group. MUST stay 0 in healthy production; any > 0 means messages may have been silently dropped.",
	},
)
