package observability

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"booking_monitor/internal/domain"
)

var (
	// --- HTTP Metrics ---
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "Duration of HTTP requests in seconds",
			// Finer buckets for accurate p99 calculation (5ms to 2.5s)
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		},
		[]string{"method", "path"},
	)

	// --- Advanced Metrics (Phase 7.7) ---

	// PageViewsTotal tracks users entering the page for conversion rate calculation.
	PageViewsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "page_views_total",
			Help: "Total number of page views to measure funnel conversion",
		},
		[]string{"page"},
	)

	// --- Business Metrics ---

	// BookingsTotal tracks booking outcomes at the API layer.
	// Labels: status = "success" | "sold_out" | "duplicate" | "error"
	BookingsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bookings_total",
			Help: "Total number of booking attempts by outcome",
		},
		[]string{"status"},
	)

	// WorkerOrdersTotal tracks order processing outcomes in the worker.
	// Labels: status = "success" | "sold_out" | "duplicate" | "db_error"
	WorkerOrdersTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "worker_orders_total",
			Help: "Total number of orders processed by the worker, by outcome",
		},
		[]string{"status"},
	)

	// WorkerProcessingDuration tracks how long the worker takes to process each message.
	WorkerProcessingDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_processing_duration_seconds",
			Help:    "Duration of worker message processing in seconds",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		},
	)

	// InventoryConflictsTotal counts how often Redis approved but DB rejected (oversell prevention).
	InventoryConflictsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "inventory_conflicts_total",
			Help: "Total number of inventory conflicts (Redis approved, DB rejected)",
		},
	)

	// DLQMessagesTotal counts messages routed to a dead-letter queue by
	// topic and reason. Labels:
	//   topic  = "order.created.dlq" | "order.failed.dlq"
	//   reason = "invalid_payload" | "invalid_event" | "max_retries"
	DLQMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dlq_messages_total",
			Help: "Total number of messages written to a dead-letter queue",
		},
		[]string{"topic", "reason"},
	)

	// SagaPoisonMessagesTotal counts saga events that exceeded the
	// compensator retry budget and were dead-lettered.
	SagaPoisonMessagesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "saga_poison_messages_total",
			Help: "Total number of saga events dead-lettered after max retries",
		},
	)

	// KafkaConsumerRetryTotal counts Kafka messages that failed to
	// process transiently and were left UNCOMMITTED for Kafka rebalance
	// to re-deliver. This is the "silent retry" surface: if this
	// counter's rate stays high, it means downstream infra (DB / Redis
	// / payment gateway) is degraded and consumers are in a stuck-but-
	// not-dead state. Labels:
	//   topic  = original Kafka topic name
	//   reason = "transient_processing_error" for now; future budget
	//            implementation will add "retry_budget_exceeded" etc.
	//
	// Paired with the `KafkaConsumerStuck` Prometheus alert which
	// fires when `rate(kafka_consumer_retry_total[5m]) > 1` for 2m.
	KafkaConsumerRetryTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kafka_consumer_retry_total",
			Help: "Total Kafka messages left uncommitted for rebalance-based retry due to transient errors",
		},
		[]string{"topic", "reason"},
	)

	// --- Infrastructure Failure Metrics (PR: worker-observability-cleanup) ---
	//
	// These counters surface log-only infra failures — rollback errors,
	// XAck / XAdd failures, failed compensation — as first-class signals.
	// Rate > 0 on any of these is an operational red flag; corresponding
	// log lines remain for post-mortem detail.

	// DBRollbackFailuresTotal increments when tx.Rollback returns a
	// non-sql.ErrTxDone error. ErrTxDone is expected after certain
	// fatal errors and is filtered at the call site.
	DBRollbackFailuresTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "db_rollback_failures_total",
			Help: "Total number of transaction rollbacks that themselves failed (excluding sql.ErrTxDone)",
		},
	)

	// RedisXAckFailuresTotal increments on XAck failure — message is
	// retained in PEL and will be re-delivered, so this counter is the
	// only leading signal that double-processing may have occurred.
	RedisXAckFailuresTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "redis_xack_failures_total",
			Help: "Total number of Redis XAck failures (message stays in PEL and will be re-delivered)",
		},
	)

	// RedisXAddFailuresTotal increments on XAdd failure, labelled by
	// target stream. Currently only DLQ writes use XAdd from Go; label
	// is kept so future main-stream writers can share this counter.
	RedisXAddFailuresTotal = promauto.NewCounterVec(
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
	RedisRevertFailuresTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "redis_revert_failures_total",
			Help: "Total number of RevertInventory failures during worker compensation (message retained in PEL)",
		},
	)

	// CacheHitsTotal / CacheMissesTotal track every cache lookup that
	// distinguishes a hit from a miss. Labelled by `cache` so we can
	// scale to multiple caches later (today only "idempotency" reads
	// before-or-after; future shapes — event detail cache, user
	// session — get their own label values).
	//
	// Hit-rate alerting is the primary use:
	//
	//   rate(cache_hits_total[5m]) /
	//   (rate(cache_hits_total[5m]) + rate(cache_misses_total[5m]))
	//
	// A sustained drop in this ratio is the canonical "cache cold" /
	// "cache wrong-keyed" / "cache being bypassed" signal — none of
	// which surface in latency or error metrics until they're severe.
	CacheHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_hits_total",
			Help: "Total number of cache lookups that returned a cached value, labelled by cache name",
		},
		[]string{"cache"},
	)

	CacheMissesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_misses_total",
			Help: "Total number of cache lookups that did not find a cached value, labelled by cache name",
		},
		[]string{"cache"},
	)

	// CacheIdempotencyOversizeTotal increments when an idempotency
	// cache Set is rejected because the marshalled value exceeds the
	// defensive size cap (maxIdempotencyValueBytes in cache/idempotency.go,
	// currently 4KB). Today no production code path triggers this —
	// the only Set caller is the booking handler emitting fixed-shape
	// JSON responses (~30-100 bytes). A non-zero rate means a future
	// caller is storing data this repo wasn't designed for.
	//
	// Pairs with the alert `IdempotencyOversize` (any non-zero rate
	// for 5m → page; this is a programmer-error signal, not a runtime
	// issue, but worth surfacing immediately).
	CacheIdempotencyOversizeTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "cache_idempotency_oversize_total",
			Help: "Idempotency Set calls rejected for exceeding the size cap (defensive; should be 0 in steady state)",
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
	RedisStreamCollectorErrorsTotal = promauto.NewCounterVec(
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
	RedisDLQRoutedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "redis_dlq_routed_total",
			Help: "Total number of messages successfully routed to orders:dlq, labelled by reason",
		},
		[]string{"reason"},
	)
)

func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Writer.Status())

		httpRequestsTotal.WithLabelValues(c.Request.Method, c.FullPath(), status).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, c.FullPath()).Observe(duration)
	}
}

// init pre-initializes all label combinations so they appear in /metrics from startup.
func init() {
	for _, status := range []string{"success", "sold_out", "duplicate", "error"} {
		BookingsTotal.WithLabelValues(status)
	}
	for _, status := range []string{"success", "sold_out", "duplicate", "db_error", "malformed_message"} {
		WorkerOrdersTotal.WithLabelValues(status)
	}
	// DLQ topic labels stay inline strings — they're Kafka-side topic
	// names (with the .dlq suffix) and have no domain-side constant.
	// If those move to domain consts in a future PR, update here too.
	for _, topic := range []string{"order.created.dlq", "order.failed.dlq"} {
		for _, reason := range []string{"invalid_payload", "invalid_event", "max_retries"} {
			DLQMessagesTotal.WithLabelValues(topic, reason)
		}
	}
	// Use the domain constants for the canonical wire event types so
	// a typo here can't drift from the producer side. The consumer
	// retry counter watches the same topic strings the producer
	// publishes via the outbox.
	for _, topic := range []string{domain.EventTypeOrderCreated, domain.EventTypeOrderFailed} {
		KafkaConsumerRetryTotal.WithLabelValues(topic, "transient_processing_error")
	}
	// Pre-warm the DLQ stream label so it appears in /metrics at startup.
	// Today "dlq" is the only value written; future main-stream writers
	// will add their own label values here.
	RedisXAddFailuresTotal.WithLabelValues("dlq")
	// Pre-warm the DLQ-route reason labels so all three series exist in
	// /metrics from boot, even on a worker that hasn't yet seen any
	// failures. Keep these strings in sync with the const block in
	// internal/infrastructure/cache/redis_queue.go (DLQReason*).
	for _, reason := range []string{"malformed_parse", "malformed_classified", "exhausted_retries"} {
		RedisDLQRoutedTotal.WithLabelValues(reason)
	}
	// Pre-warm the cache labels so the series exist in /metrics before
	// the first lookup. "idempotency" is the only cache today; add new
	// values here when new caches are introduced.
	for _, cache := range []string{"idempotency"} {
		CacheHitsTotal.WithLabelValues(cache)
		CacheMissesTotal.WithLabelValues(cache)
	}

	// Pre-warm reconciler counter labels so the series exist in
	// /metrics from the first /metrics scrape, even before the
	// reconciler has resolved a single order. Lets dashboards show
	// "0 errors so far" instead of "no data" — a real distinction.
	for _, outcome := range []string{"charged", "declined", "not_found", "unknown", "max_age_exceeded", "transition_lost"} {
		ReconResolvedTotal.WithLabelValues(outcome)
	}
}

// --- A4 Reconciler Metrics ---

// ReconResolvedTotal counts orders the reconciler resolved per sweep,
// labelled by outcome:
//
//	"charged"           — gateway said charged, MarkConfirmed succeeded
//	"declined"          — gateway said declined, MarkFailed succeeded
//	"not_found"         — gateway has no record, transitioned to Failed
//	"unknown"           — gateway returned an unclassified verdict, skipped (will retry)
//	"max_age_exceeded"  — Charging older than RECON_MAX_CHARGING_AGE, force-failed
//	"transition_lost"   — Mark{Confirmed,Failed} returned ErrInvalidTransition
//	                      (the row moved between FindStuckCharging and Mark*) — benign,
//	                      counted so a sustained rate can be alerted on as a
//	                      design-assumption violation
//
// "transient_error" is NOT a value here — that goes to ReconGatewayErrorsTotal
// because it tracks infrastructure failures (network, gateway 5xx) which
// are operationally distinct from per-order verdicts.
var ReconResolvedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "recon_resolved_total",
		Help: "Total orders resolved by the reconciler per sweep, by outcome",
	},
	[]string{"outcome"},
)

// ReconGatewayErrorsTotal increments on infrastructure failures during
// gateway.GetStatus — network errors, gateway 5xx, ctx timeout. The
// reconciler treats these as transient (retry next sweep) but emits
// the counter so a sustained rate is alertable. Distinct from
// ReconResolvedTotal{outcome="unknown"} which is a successful gateway
// call that returned an unclassifiable verdict.
var ReconGatewayErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "recon_gateway_errors_total",
		Help: "Total gateway.GetStatus infrastructure failures (network, 5xx, timeout)",
	},
)

// ReconMarkErrorsTotal increments when MarkConfirmed / MarkFailed
// returns a non-ErrInvalidTransition error during reconciler resolve
// — i.e., a real DB failure (connection lost, deadlock, integrity
// constraint). ErrInvalidTransition itself is benign (race-loss to
// the worker; counted as `transition_lost` outcome instead).
//
// Without this counter, sustained DB failures during the resolve
// path would only surface in logs — no alert can fire on log lines.
var ReconMarkErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "recon_mark_errors_total",
		Help: "Total Mark{Confirmed,Failed} DB failures during recon resolve (excludes ErrInvalidTransition)",
	},
)

// ReconFindStuckErrorsTotal fires when the per-sweep
// FindStuckCharging query itself errors (DB outage, missing index
// from a forgotten migration, query timeout). Without this counter,
// a sustained sweep-query failure shows nothing on dashboards
// EXCEPT a stale `recon_stuck_charging_orders` gauge — operators
// can't distinguish "orders are stuck" from "recon itself is broken".
//
// Pairs with the alert: `rate(recon_find_stuck_errors_total[5m]) > 0`
// fires immediately on any sustained failure.
var ReconFindStuckErrorsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "recon_find_stuck_errors_total",
		Help: "Total FindStuckCharging query failures (DB outage, missing index, timeout)",
	},
)

// ReconStuckChargingOrders is a gauge of orders currently in Charging
// state older than RECON_CHARGING_THRESHOLD. Reset to the latest count
// on every sweep. Pairs with the alert
// `stuck_charging_orders > 0 for 5m` — sustained value = recon falling
// behind or systemic gateway degradation.
var ReconStuckChargingOrders = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "recon_stuck_charging_orders",
		Help: "Charging orders older than the threshold, set on each reconciler sweep",
	},
)

// ReconResolveDurationSeconds is a histogram of how long an
// individual stuck order took to resolve (FindStuckCharging time of
// detection → final Mark{X} commit). Drives the
// `recon_charging_resolve_age_seconds` p50/p95/p99 dashboard which
// is the primary signal for tuning RECON_CHARGING_THRESHOLD: if p95
// regularly exceeds your threshold, your threshold is too aggressive
// and you're stealing in-flight orders from the worker.
//
// Note this is NOT the order's age in Charging; that's
// `recon_resolve_age_seconds`. This is the resolve operation's
// own duration.
var ReconResolveDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "recon_resolve_duration_seconds",
		Help:    "Wall-clock duration of a single stuck-order resolve operation",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
)

// ReconResolveAgeSeconds is a histogram of the AGE of orders at
// resolve time (NOW() - updated_at when we picked them up). The key
// signal for tuning RECON_CHARGING_THRESHOLD downward: if p50 is
// 130s and threshold is 120s, recon is consistently catching orders
// 10s after they cross — which means orders are in Charging for
// 130s+ before resolving, suggesting a slow gateway. Tighten or
// add an upstream alert.
var ReconResolveAgeSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "recon_resolve_age_seconds",
		Help:    "Age of stuck orders at the moment the reconciler resolves them",
		Buckets: []float64{30, 60, 90, 120, 180, 300, 600, 1800, 3600, 21600, 86400}, // 30s … 24h
	},
)

// ReconGatewayDurationSeconds is a histogram of gateway.GetStatus
// latency observed by the reconciler. Drives RECON_GATEWAY_TIMEOUT
// tuning: p99 should be well below the timeout. A long tail indicates
// a slow gateway, which probably also affects the worker's Charge
// path.
var ReconGatewayDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "recon_gateway_get_status_duration_seconds",
		Help:    "Wall-clock duration of gateway.GetStatus calls from the reconciler",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
)
