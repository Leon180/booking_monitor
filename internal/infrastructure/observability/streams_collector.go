package observability

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// StreamsCollector exposes Redis Stream backlog signals as Prometheus
// gauges read at scrape time. Implements `prometheus.Collector`
// directly (same shape as DBPoolCollector) so the readings are
// on-demand, not held by a polling goroutine.
//
// Why these metrics matter (and why we don't yet have them today):
// the existing redis_xack_failures_total / redis_xadd_failures_total
// counters surface processing-side failures, but operators can't see
// "the queue is backed up" until p95 booking latency spikes. With
// these gauges, a stuck consumer / slow worker / poison-message
// storm shows up immediately as a stream length climb.
//
// Three gauges per (stream, group) pair:
//
//	redis_stream_length{stream}                 → XLEN          (O(1))
//	redis_stream_pending_entries{stream,group}  → XPENDING summary
//	redis_stream_consumer_lag_seconds{stream,group} → derived from
//	                                               Higher pending ID
//
// Lag derivation: stream entry IDs in Redis are `<ms-since-epoch>-<seq>`.
// The summary `XPENDING <stream> <group>` returns the smallest
// (`Lower`) and largest (`Higher`) pending IDs. We parse `Lower`
// (the OLDEST unacknowledged entry) as a millisecond timestamp and
// emit `now - <ms>` as the "consumer lag" estimate. This is the
// canonical Redis Streams lag signal — "how far behind the consumer
// is on the oldest unprocessed entry it owns" — and matches the
// `OrdersStreamConsumerLag` Prometheus alert's semantics.
//
// Cost: 2 Redis round-trips per stream per scrape (XLEN + XPENDING).
// At Prometheus default scrape interval (15s) and 2 streams
// (orders:stream + orders:dlq), that's 4 round-trips every 15s
// (~0.27/sec). Negligible vs booking hot path traffic.
//
// Future enhancement (B1 work): autoscaler reads
// rate(redis_stream_length[1m]) to scale workers on demand.
type StreamsCollector struct {
	client  *redis.Client
	streams []streamGroup
	timeout time.Duration

	lengthDesc  *prometheus.Desc
	pendingDesc *prometheus.Desc
	lagDesc     *prometheus.Desc
}

// streamGroup couples a stream with its consumer group. DLQ streams
// have no group (Group="") — only XLEN is queried, no pending/lag.
type streamGroup struct {
	Stream string
	Group  string
}

// NewStreamsCollector constructs the collector for the canonical
// streams in this project: orders:stream (with orders:group) +
// orders:dlq (no consumer group — DLQ is operator-pull, not consumer-
// driven). Adding more streams = appending to streamGroups.
//
// scrapeTimeout caps each Prometheus scrape's Redis-call wall clock so
// a slow/dead Redis cannot pin the scrape thread. Below the typical
// scrape_interval (15s) by an order of magnitude.
func NewStreamsCollector(client *redis.Client) *StreamsCollector {
	return &StreamsCollector{
		client:  client,
		timeout: 1 * time.Second,
		streams: []streamGroup{
			{Stream: "orders:stream", Group: "orders:group"},
			{Stream: "orders:dlq", Group: ""}, // no group; XLEN only
		},
		lengthDesc: prometheus.NewDesc(
			"redis_stream_length",
			"Number of entries currently stored in the stream (XLEN). Hot streams should stay near 0; sustained > 0 = consumer backlog.",
			[]string{"stream"}, nil,
		),
		pendingDesc: prometheus.NewDesc(
			"redis_stream_pending_entries",
			"Pending entries (XPENDING summary count) per consumer group — messages delivered but not yet XACKed. > 0 = work in flight.",
			[]string{"stream", "group"}, nil,
		),
		lagDesc: prometheus.NewDesc(
			"redis_stream_consumer_lag_seconds",
			"Wall-clock age of the oldest pending entry the consumer group owns (NOW() - oldest-pending-id-ms). The canonical Redis Streams lag signal.",
			[]string{"stream", "group"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *StreamsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.lengthDesc
	ch <- c.pendingDesc
	ch <- c.lagDesc
}

// Collect implements prometheus.Collector. Errors from individual
// Redis calls are logged-only via swallow — emitting partial metric
// sets is preferable to failing the whole scrape, since dashboards
// degrade gracefully on missing series.
//
// Trade-off: we don't surface Redis-call errors here as a separate
// counter today because (a) they would just duplicate the existing
// redis_xack/xadd failure counters' semantics, and (b) a reading
// failure here is invisible to /metrics — the alert that catches it
// is "stream length series went stale", which Prometheus already
// detects via its own scrape-staleness machinery.
func (c *StreamsCollector) Collect(ch chan<- prometheus.Metric) {
	// prometheus.Collector.Collect has no ctx parameter — the
	// scrape's deadline isn't propagated to us. Use a fresh
	// background ctx with our own timeout so a stuck/slow Redis
	// can't pin the scrape thread for longer than c.timeout (1s).
	// Below the default Prometheus scrape_interval (15s) by an
	// order of magnitude, so the bound is safely tight.
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	for _, sg := range c.streams {
		// XLEN — O(1), always emit. On failure, increment the
		// dedicated errors counter so a sustained Redis outage
		// surfaces in dashboards even though the length/pending/lag
		// gauges fall silent. See RedisStreamCollectorErrorsTotal
		// godoc for rationale (silent-failure-hunter F1 fix).
		if n, err := c.client.XLen(ctx, sg.Stream).Result(); err == nil {
			ch <- prometheus.MustNewConstMetric(
				c.lengthDesc, prometheus.GaugeValue, float64(n), sg.Stream,
			)
		} else {
			RedisStreamCollectorErrorsTotal.WithLabelValues(sg.Stream, "xlen").Inc()
		}

		if sg.Group == "" {
			continue // DLQ has no consumer group; skip pending/lag
		}

		// XPENDING summary — O(1), returns count + Lower/Higher IDs.
		pending, err := c.client.XPending(ctx, sg.Stream, sg.Group).Result()
		if err != nil {
			RedisStreamCollectorErrorsTotal.WithLabelValues(sg.Stream, "xpending").Inc()
			continue
		}
		if pending == nil {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.pendingDesc, prometheus.GaugeValue, float64(pending.Count),
			sg.Stream, sg.Group,
		)

		// Derive lag from the OLDEST pending entry's ID timestamp.
		// `pending.Lower` is the smallest pending ID — i.e., the
		// oldest in-flight message. NOW() - Lower's ms = how far
		// behind the consumer is on the oldest work it owns.
		if pending.Lower != "" {
			if ageMs, ok := streamIDAgeMillis(pending.Lower); ok {
				ch <- prometheus.MustNewConstMetric(
					c.lagDesc, prometheus.GaugeValue, float64(ageMs)/1000.0,
					sg.Stream, sg.Group,
				)
			}
		}
	}
}

// streamIDAgeMillis parses a Redis Stream entry ID of the form
// "<ms>-<seq>" and returns NOW() - <ms> in milliseconds.
//
// Returns (0, false) on parse failure so the caller skips the metric
// rather than emitting a wildly-wrong value (e.g., 1.7e12 if we
// accidentally interpreted the ID as already-elapsed-seconds).
func streamIDAgeMillis(id string) (int64, bool) {
	dashIdx := strings.IndexByte(id, '-')
	if dashIdx < 1 {
		return 0, false
	}
	ms, err := strconv.ParseInt(id[:dashIdx], 10, 64)
	if err != nil || ms <= 0 {
		return 0, false
	}
	now := time.Now().UnixMilli()
	if now < ms {
		// Clock skew; clamp to 0 rather than emit negative lag.
		return 0, true
	}
	return now - ms, true
}
