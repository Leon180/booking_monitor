package cache

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"booking_monitor/internal/application/admin"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// Default values for admin event bus configuration. Override via
// AdminEventBusConfig when constructing.
//
// Channel capacity = 10,000 ≈ 1.8s buffer at Stage 5 ceiling
// (5,533 events/s). MAXLEN = 100,000 ≈ 18s burst history or weeks
// at idle. XAdd timeout = 2s is a per-call budget; the drainer
// loops back to the next event on timeout and increments the
// xadd_failures counter.
//
// See docs/design/admin_event_streaming.md § Q2, Q3.
const (
	DefaultAdminStreamKey    = "events:admin:stream"
	DefaultAdminStreamMaxLen = 100_000
	DefaultAdminChannelCap   = 10_000
	DefaultAdminXAddTimeout  = 2 * time.Second
	DefaultAdminDrainTimeout = 5 * time.Second

	// depthGaugeInterval is how often the live channel-depth gauge
	// is refreshed. 1s is fine — Prometheus scrapes at 15s typically.
	depthGaugeInterval = 1 * time.Second
)

// AdminEventBusConfig is the tunable surface for the bus.
type AdminEventBusConfig struct {
	StreamKey       string
	StreamMaxLen    int64
	ChannelCapacity int
	XAddTimeout     time.Duration
	DrainTimeout    time.Duration
}

// DefaultAdminEventBusConfig returns the production defaults.
func DefaultAdminEventBusConfig() AdminEventBusConfig {
	return AdminEventBusConfig{
		StreamKey:       DefaultAdminStreamKey,
		StreamMaxLen:    DefaultAdminStreamMaxLen,
		ChannelCapacity: DefaultAdminChannelCap,
		XAddTimeout:     DefaultAdminXAddTimeout,
		DrainTimeout:    DefaultAdminDrainTimeout,
	}
}

// adminEventBus is the Redis Streams-backed implementation of
// admin.Bus. Bounded-async with drop policy (OTel BatchSpanProcessor
// pattern); single drainer goroutine.
//
// See docs/design/admin_event_streaming.md § Q3.
type adminEventBus struct {
	client redis.UniversalClient
	cfg    AdminEventBusConfig
	ch     chan admin.AdminEvent
	log    *mlog.Logger
	depth  atomic.Int64
	done   chan struct{} // closed when Run() returns
}

// Compile-time check that the type satisfies admin.Bus.
var _ admin.Bus = (*adminEventBus)(nil)

// NewAdminEventBus constructs a Redis-backed admin event bus.
// Caller MUST start the drainer goroutine via go bus.Run(ctx).
// fx.Lifecycle wiring is in internal/bootstrap/admin_stream.go.
func NewAdminEventBus(client redis.UniversalClient, cfg AdminEventBusConfig, logger *mlog.Logger) *adminEventBus {
	if cfg.StreamKey == "" {
		cfg.StreamKey = DefaultAdminStreamKey
	}
	if cfg.StreamMaxLen == 0 {
		cfg.StreamMaxLen = DefaultAdminStreamMaxLen
	}
	if cfg.ChannelCapacity == 0 {
		cfg.ChannelCapacity = DefaultAdminChannelCap
	}
	if cfg.XAddTimeout == 0 {
		cfg.XAddTimeout = DefaultAdminXAddTimeout
	}
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = DefaultAdminDrainTimeout
	}
	return &adminEventBus{
		client: client,
		cfg:    cfg,
		ch:     make(chan admin.AdminEvent, cfg.ChannelCapacity),
		log:    logger.With(mlog.String("component", "admin_event_bus")),
		done:   make(chan struct{}),
	}
}

// Publish enqueues an event onto the internal channel. Non-blocking;
// drops the event and increments admin_event_bus_dropped_total on
// channel-full. Logging is intentionally OMITTED here (would flood
// logs under sustained backpressure); operators observe via the
// metric counter and Prometheus alerts.
//
// Safe for concurrent use.
func (b *adminEventBus) Publish(evt admin.AdminEvent) {
	select {
	case b.ch <- evt:
		b.depth.Add(1)
		observability.AdminEventBusPublishedTotal.WithLabelValues(evt.EventType).Inc()
	default:
		observability.AdminEventBusDroppedTotal.WithLabelValues("channel_full").Inc()
	}
}

// Done returns a channel closed when Run() exits. Used by bootstrap
// to coordinate shutdown.
func (b *adminEventBus) Done() <-chan struct{} { return b.done }

// Run is the drainer loop. Blocks until ctx is cancelled, then
// attempts to drain remaining events with a bounded timeout before
// returning. Closes Done() channel on exit.
//
// This is the only goroutine that reads from b.ch — so b.depth.Add
// is only called from Publish (increment) and this goroutine
// (decrement), no concurrent decrement.
func (b *adminEventBus) Run(ctx context.Context) {
	defer close(b.done)

	b.log.Info(ctx, "admin event bus drainer starting",
		mlog.String("stream_key", b.cfg.StreamKey),
		mlog.Int("channel_capacity", b.cfg.ChannelCapacity),
		mlog.Int64("stream_maxlen", b.cfg.StreamMaxLen),
	)

	gaugeTicker := time.NewTicker(depthGaugeInterval)
	defer gaugeTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			b.drainOnShutdown()
			return
		case <-gaugeTicker.C:
			observability.AdminEventBusChannelDepth.Set(float64(b.depth.Load()))
		case evt := <-b.ch:
			b.depth.Add(-1)
			b.xadd(ctx, evt)
		}
	}
}

// xadd does one XADD with the bus's configured per-call timeout.
// Errors are logged + counted; events are NOT retried (admin
// observability tolerates loss — PG audit is SSOT).
func (b *adminEventBus) xadd(parent context.Context, evt admin.AdminEvent) {
	ctx, cancel := context.WithTimeout(parent, b.cfg.XAddTimeout)
	defer cancel()

	err := b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: b.cfg.StreamKey,
		MaxLen: b.cfg.StreamMaxLen,
		Approx: true, // MAXLEN ~ N: trim is approximate, faster
		Values: map[string]any{
			"event_id":       evt.EventID.String(),
			"event_type":     evt.EventType,
			"occurred_at":    evt.OccurredAt.Format(time.RFC3339Nano),
			"schema_version": evt.SchemaVersion,
			"data":           string(evt.Data),
		},
	}).Err()

	if err != nil {
		reason := classifyXAddError(err)
		observability.AdminEventBusXAddFailuresTotal.WithLabelValues(reason).Inc()
		b.log.Error(parent, "admin event bus xadd failed",
			tag.Error(err),
			mlog.String("event_type", evt.EventType),
			mlog.String("event_id", evt.EventID.String()),
			mlog.String("reason", reason),
		)
	}
}

// drainOnShutdown attempts to flush remaining channel events to
// Redis with a separate deadline (DrainTimeout). Independent of
// the parent ctx — parent is already cancelled at this point.
//
// Stops on whichever happens first: channel empty, drain timeout.
// Abandoned events are logged with count for postmortem.
func (b *adminEventBus) drainOnShutdown() {
	drainCtx, cancel := context.WithTimeout(context.Background(), b.cfg.DrainTimeout)
	defer cancel()

	initialDepth := b.depth.Load()
	b.log.Info(drainCtx, "admin event bus drainer beginning shutdown drain",
		mlog.Int64("pending_events", initialDepth),
		mlog.Duration("drain_timeout", b.cfg.DrainTimeout),
	)

	drained := 0
	for {
		select {
		case <-drainCtx.Done():
			remaining := b.depth.Load()
			b.log.Warn(context.Background(),
				"admin event bus drainer abandoned events at shutdown timeout",
				mlog.Int("drained", drained),
				mlog.Int64("abandoned", remaining),
			)
			return
		case evt := <-b.ch:
			b.depth.Add(-1)
			b.xadd(drainCtx, evt)
			drained++
		default:
			// Channel empty — clean exit
			b.log.Info(context.Background(),
				"admin event bus drainer shutdown drain complete",
				mlog.Int("drained", drained),
			)
			return
		}
	}
}

// classifyXAddError maps a Redis client error to one of the
// metric label values pre-warmed in observability/metrics_init.go.
// Unknown errors land in "other" — investigate if that label
// dominates.
func classifyXAddError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case strings.Contains(err.Error(), "connection refused"),
		strings.Contains(err.Error(), "no such host"),
		strings.Contains(err.Error(), "i/o timeout"):
		return "conn_refused"
	default:
		return "other"
	}
}
