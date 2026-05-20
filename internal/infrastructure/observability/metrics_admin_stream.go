package observability

// Admin event streaming metrics — Layer B infrastructure observability
// for the SSE admin event stream pipeline. These tell SRE/platform
// engineers whether the streaming layer itself is healthy; they are
// distinct from Layer A business metrics (bookings_total / orders_paid /
// etc.) which already exist elsewhere in this package.
//
// See `docs/design/admin_event_streaming.md` § Q16 for the layering
// rationale. The accompanying business gap (inventory.low) is in
// metrics_inventory_low.go.
//
// Naming convention matches sibling concern-scoped prefixes
// (`payment_webhook_*`, `saga_*`, `stripe_*`, `expiry_*`) rather than
// the misleading `booking_*` global prefix.

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ─── Bus layer (publisher-side) ─────────────────────────────────────

// AdminEventBusPublishedTotal counts events successfully enqueued onto
// the in-memory bus channel. NOT the same as "XADD to Redis" — that's
// counted via AdminEventBusChannelDepth (drain rate) and XAdd failures
// separately. This counter increments at the moment `bus.Publish()`
// returns without dropping.
var AdminEventBusPublishedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "admin_event_bus_published_total",
		Help: "Events successfully enqueued onto the admin event bus channel, by event type.",
	},
	[]string{"event_type"},
)

// AdminEventBusDroppedTotal counts events dropped due to backpressure.
// The "channel_full" reason fires when the bounded channel (cap=10000
// per Q3 design) is at capacity and Publish() is forced to drop. A
// sustained non-zero rate signals that the drainer goroutine cannot
// keep up with publish rate (typically Redis XADD is the bottleneck).
//
// Alert recipe: rate(admin_event_bus_dropped_total[5m]) > 0 for 2m.
var AdminEventBusDroppedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "admin_event_bus_dropped_total",
		Help: "Events dropped at the bus layer due to backpressure.",
	},
	[]string{"reason"},
)

// AdminEventBusXAddFailuresTotal counts XADD failures inside the
// drainer goroutine. Distinct from AdminEventBusDroppedTotal:
//   - dropped: Publish() couldn't enqueue (channel full)
//   - xadd_failures: drainer dequeued but Redis rejected the XADD
//
// Both indicate degradation but at different stages.
var AdminEventBusXAddFailuresTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "admin_event_bus_xadd_failures_total",
		Help: "Redis XADD failures observed by the admin event bus drainer.",
	},
	[]string{"reason"},
)

// AdminEventBusChannelDepth exposes the current depth of the in-memory
// bus channel. Scraped at scrape_interval (default 15s); useful for
// dashboards showing real-time backpressure. Steady-state depth should
// be near 0 — sustained high depth means drainer is falling behind.
var AdminEventBusChannelDepth = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "admin_event_bus_channel_depth",
		Help: "Current depth of the admin event bus channel (live gauge).",
	},
)

// ─── SSE handler layer ──────────────────────────────────────────────

// AdminSSEActiveConnections is the number of admin clients currently
// connected to /api/v1/admin/events/stream. Incremented at handler
// entry, decremented in defer (HTTP context cancellation path).
var AdminSSEActiveConnections = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "admin_sse_active_connections",
		Help: "Currently connected admin SSE clients.",
	},
)

// AdminSSEMessagesSentTotal counts events successfully written to the
// HTTP response (flushed). Labeled by event_type so dashboards can
// show event-class breakdown. Heartbeats are tracked separately via
// AdminSSEHeartbeatsSentTotal to keep this counter focused on
// actual business events.
var AdminSSEMessagesSentTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "admin_sse_messages_sent_total",
		Help: "Admin SSE events successfully sent to clients, by event type.",
	},
	[]string{"event_type"},
)

// AdminSSEClientsDroppedTotal counts clients forcibly disconnected by
// the hub. Per Q10 design, a slow client whose `c.send` channel fills
// up gets dropped (close c.send, delete from registry) rather than
// blocking the broadcast loop.
//
// Reasons:
//   - "slow_consumer"  : c.send full at broadcast time (Q10 main path)
//   - "write_error"    : flush failed (TCP error / client gone)
//   - "shutdown"       : graceful drain (Q15)
var AdminSSEClientsDroppedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "admin_sse_clients_dropped_total",
		Help: "Admin SSE clients disconnected by the hub.",
	},
	[]string{"reason"},
)

// AdminSSEReconnectsTotal counts incoming connections with a
// `Last-Event-ID` header set (i.e., resume attempts). A burst of
// reconnects right after deploy is expected (per Q15 graceful
// shutdown sends `retry:` hint); sustained high rate indicates
// network instability or LB issues.
var AdminSSEReconnectsTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "admin_sse_reconnects_total",
		Help: "Admin SSE connection attempts with a Last-Event-ID (resumptions).",
	},
)

// AdminSSEHeartbeatsSentTotal counts heartbeat comment-lines emitted
// (per Q13, every 30s per active connection). Useful for spotting
// stuck writer goroutines: if active_connections > 0 but heartbeats
// flatline, a writer is wedged.
var AdminSSEHeartbeatsSentTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "admin_sse_heartbeats_sent_total",
		Help: "Heartbeat comment-lines sent to admin SSE clients.",
	},
)

// AdminSSEEventsTruncatedTotal counts reconnects where the client's
// `Last-Event-ID` is older than the stream's MAXLEN trim window, so
// the server cannot replay the missed events. The handler emits a
// synthetic `stream_truncated` event in this case (per Q8 edge cases).
//
// Non-zero values during a flash sale are expected (burst exceeds the
// MAXLEN of ~100k). Sustained non-zero outside flash periods suggests
// network outages causing clients to lag beyond replay window.
var AdminSSEEventsTruncatedTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "admin_sse_events_truncated_total",
		Help: "Reconnects where Last-Event-ID was already trimmed from the stream.",
	},
)

// AdminSSEConnectionDurationSeconds is a histogram of admin SSE
// connection lifetimes. Useful for understanding ops viewing patterns
// (do they keep tabs open all day? brief checks?). Bucket range
// covers seconds-to-hours since admin sessions can be very long.
var AdminSSEConnectionDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "admin_sse_connection_duration_seconds",
		Help:    "Duration of admin SSE connections from accept to close.",
		Buckets: []float64{1, 5, 30, 60, 300, 1800, 3600, 18000},
	},
)

// AdminSSEMessageLagSeconds is a histogram of latency from event
// creation (AdminEvent.OccurredAt) to delivery to a connected client.
// p95 should be well under 1s in healthy state; values approaching 1s
// indicate backpressure somewhere in the pipeline.
//
// Bucket range covers sub-millisecond (XADD-fast) to seconds
// (clearly degraded).
var AdminSSEMessageLagSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "admin_sse_message_lag_seconds",
		Help:    "Latency from admin event creation to client delivery.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
	},
)

// ─── Subscriber layer (Redis XREAD goroutine) ───────────────────────

// AdminStreamSubscriberConsecFailures tracks the current consecutive
// failure count of the Redis XREAD goroutine. Resets to 0 on every
// successful read or `redis.Nil` (idle timeout). Per Q11 design,
// crossing the configured threshold (default 30) triggers
// fx.Shutdown(ExitCode(1)) to let k8s restart the pod.
//
// Alert recipe: admin_stream_subscriber_consec_failures > 10 for 1m.
// This catches degradation before fx.Shutdown fires at 30, giving
// ops a heads-up.
var AdminStreamSubscriberConsecFailures = promauto.NewGauge(
	prometheus.GaugeOpts{
		Name: "admin_stream_subscriber_consec_failures",
		Help: "Current consecutive XREAD failure count for the admin event stream subscriber.",
	},
)

// AdminStreamSubscriberXReadFailuresTotal is the cumulative XREAD
// failure counter. Tracks all-time failures (cleared only by process
// restart). Use rate() in PromQL for current rate.
var AdminStreamSubscriberXReadFailuresTotal = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "admin_stream_subscriber_xread_failures_total",
		Help: "Total XREAD failures observed by the admin event stream subscriber.",
	},
)
