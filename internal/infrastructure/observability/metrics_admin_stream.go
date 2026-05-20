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
	"sync"

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

// ─── Resource-level gauges (PR #122 follow-up) ──────────────────────
//
// Per-subsystem resource isolation: the global `go_goroutines` and
// `go_memstats_alloc_bytes` gauges include every goroutine and every
// allocation in the process, mixing SSE-subsystem usage with worker,
// outbox relay, recon, etc. These admin-stream-scoped gauges isolate
// the SSE pipeline's footprint so capacity planning and incident
// triage can answer "is the streaming layer using more resources
// than expected?" directly.
//
// Pattern: prometheus.NewGaugeFunc with a callback that reads atomic
// counters maintained by the hub / bus. No OSS package provides this
// (verified 2026-05-20 research) — every Go service rolls its own
// per the canonical NewGaugeFunc pattern.

// AdminSSEEstimatedMemoryBytes is a coarse estimate of the
// memory footprint of the SSE subsystem, computed as:
//
//	N_clients × (8 KB goroutine stack + ClientSendBufferCapacity × avgMsgSize)
//
// The estimate is intentionally rough — its purpose is to flag
// runaway growth (e.g., 100 → 10,000 clients) for capacity planning,
// not to replace pprof for precise heap analysis. The callback reads
// the hub's atomic active-client counter; safe to call frequently.
var AdminSSEEstimatedMemoryBytes prometheus.GaugeFunc

// AdminEventBusChannelHighWatermark records the maximum bus-channel
// depth observed since process start. Useful for capacity planning:
// if HWM trends upward toward the configured capacity (10000), the
// drainer is falling behind sustained spikes and channel size or
// drainer throughput needs review.
var AdminEventBusChannelHighWatermark prometheus.GaugeFunc

// AdminHubBroadcastHighWatermark mirrors the above for the hub's
// broadcast channel (cap 256). If HWM > 200 in production, the hub
// loop is being outpaced by the subscriber goroutine, suggesting
// slow-client backpressure or hub broadcast contention.
var AdminHubBroadcastHighWatermark prometheus.GaugeFunc

// AdminSSEWriteMessageDurationSeconds is a histogram of per-frame
// SSE write latency from `writeMessage()`. Useful for spotting slow
// clients: if p99 climbs while p50 is fine, individual clients are
// throttling but the writer goroutine itself is healthy.
//
// Buckets cover sub-millisecond writes (fast happy path) through
// 100 ms (TCP backpressure / wire write blocking on slow client).
var AdminSSEWriteMessageDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "admin_sse_write_message_duration_seconds",
		Help:    "Duration of writeMessage() per SSE frame.",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5},
	},
)

// AdminEventBusXAddDurationSeconds is a histogram of per-XADD
// latency from the bus drainer. Useful for spotting Redis-side
// pressure: if p99 climbs > XAddTimeout (default 2s), the drainer
// is hitting timeouts.
//
// Buckets cover sub-millisecond (warm Redis, in-memory) through
// 2 s (close to the XAddTimeout cap).
var AdminEventBusXAddDurationSeconds = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "admin_event_bus_xadd_duration_seconds",
		Help:    "Duration of XADD call from the admin event bus drainer.",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2},
	},
)

// RegisterAdminStreamResourceGauges installs the three callback
// gauges (memory, bus HWM, hub HWM). Called once at app startup
// from bootstrap; the callbacks close over the supplied accessor
// functions, which read internal atomic counters maintained by the
// hub / bus.
//
// Caller signature is a thin interface (just three func() int64)
// to avoid an import cycle between observability and sse/cache
// packages.
type AdminStreamResourceSources struct {
	ActiveClients         func() int64
	ClientSendCapacity    int64
	AvgMsgSize            int64
	BusChannelHighWater   func() int64
	HubBroadcastHighWater func() int64
}

// registerOnce serialises RegisterAdminStreamResourceGauges so a
// second call is a no-op instead of a Prometheus duplicate-registration
// panic. The fx wiring in bootstrap calls this function exactly once,
// but tests that import the package — or future fx-module composition
// — may inadvertently call twice; this guard turns the foot-gun into a
// no-op rather than a runtime panic.
var registerOnce sync.Once

// RegisterAdminStreamResourceGauges wires the GaugeFunc callbacks.
// NOT idempotent across distinct argument sets — the first call wins;
// subsequent calls are skipped via sync.Once. Returns true on the
// first (registering) call, false on every subsequent call.
//
// Callers SHOULD check the return value and log on false so that a
// second call (e.g., a test spinning up two AdminStreamModule fx
// apps in one binary, or an accidental double-invoke) is observable
// — otherwise the second instance's accessor functions are silently
// dropped and the metrics keep reflecting the first instance only.
//
// Production callers MUST call from a single bootstrap site
// (currently internal/bootstrap/admin_stream.go's
// registerAdminStreamResourceGauges fx.Invoke).
func RegisterAdminStreamResourceGauges(s AdminStreamResourceSources) bool {
	registered := false
	registerOnce.Do(func() {
		registered = true
		const goroutineStackBytes = 8192 // typical Go stack baseline per goroutine
		AdminSSEEstimatedMemoryBytes = promauto.NewGaugeFunc(
			prometheus.GaugeOpts{
				Name: "admin_sse_estimated_memory_bytes",
				Help: "Coarse estimate of SSE subsystem memory: active_clients × (goroutine_stack + send_buffer_bytes).",
			},
			func() float64 {
				n := s.ActiveClients()
				return float64(n * (goroutineStackBytes + s.ClientSendCapacity*s.AvgMsgSize))
			},
		)
		AdminEventBusChannelHighWatermark = promauto.NewGaugeFunc(
			prometheus.GaugeOpts{
				Name: "admin_event_bus_channel_high_water_mark",
				Help: "Max admin event bus channel depth observed since process start.",
			},
			func() float64 { return float64(s.BusChannelHighWater()) },
		)
		AdminHubBroadcastHighWatermark = promauto.NewGaugeFunc(
			prometheus.GaugeOpts{
				Name: "admin_hub_broadcast_high_water_mark",
				Help: "Max hub broadcast channel depth observed since process start.",
			},
			func() float64 { return float64(s.HubBroadcastHighWater()) },
		)
	})
	return registered
}
