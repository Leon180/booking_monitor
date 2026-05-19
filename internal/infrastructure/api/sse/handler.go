package sse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// Default handler configuration.
const (
	DefaultHandlerHeartbeatInterval = 30 * time.Second
	DefaultHandlerReplayMaxCount    = 1000
	DefaultHandlerRetryHintMinMS    = 2000
	DefaultHandlerRetryHintMaxMS    = 8000
)

// HandlerConfig is the tunable surface for the SSE handler.
type HandlerConfig struct {
	StreamKey         string
	HeartbeatInterval time.Duration
	ReplayMaxCount    int64
	RetryHintMinMS    int
	RetryHintMaxMS    int
}

// DefaultHandlerConfig returns the production defaults using the given
// stream key.
func DefaultHandlerConfig(streamKey string) HandlerConfig {
	return HandlerConfig{
		StreamKey:         streamKey,
		HeartbeatInterval: DefaultHandlerHeartbeatInterval,
		ReplayMaxCount:    DefaultHandlerReplayMaxCount,
		RetryHintMinMS:    DefaultHandlerRetryHintMinMS,
		RetryHintMaxMS:    DefaultHandlerRetryHintMaxMS,
	}
}

// Handler serves the admin event SSE stream. Implements the
// subscribe-then-replay pattern (Q8): registers client with hub
// first, then XRANGE-replays history from Last-Event-ID. The hub
// buffers live events into client.Send while replay runs; when
// the writer loop starts consuming, history and live events
// arrive in stream order.
type Handler struct {
	client redis.UniversalClient
	hub    *Hub
	cfg    HandlerConfig
	log    *mlog.Logger

	// shuttingDown is set by OnStop. Once true, new connections
	// receive 503 (Q15 reject-new step).
	shuttingDown atomic.Bool
}

// NewHandler constructs the SSE handler.
func NewHandler(client redis.UniversalClient, hub *Hub, cfg HandlerConfig, logger *mlog.Logger) *Handler {
	applyHandlerDefaults(&cfg)
	return &Handler{
		client: client,
		hub:    hub,
		cfg:    cfg,
		log:    logger.With(mlog.String("component", "admin_sse_handler")),
	}
}

// SetShuttingDown flips the reject-new flag. Called by bootstrap
// OnStop before draining the hub.
func (h *Handler) SetShuttingDown(v bool) { h.shuttingDown.Store(v) }

// BroadcastRetryHints sends a jittered `retry:` line to every
// connected client through the hub broadcast channel. Per Q15
// graceful-shutdown, this lets EventSource clients reconnect at
// staggered times rather than synchronously at the default 3s.
//
// The "event type" of a retry hint is the special internal name
// "_retry_hint" carried in the redis.XMessage Values. The handler's
// writer loop recognizes this and emits only the `retry:` line.
//
// Uses math/rand/v2 for jitter (non-cryptographic; we just want
// distribution-friendly random delays, not secrets — gosec G404
// is suppressed below).
func (h *Handler) BroadcastRetryHints(ctx context.Context) {
	//nolint:gosec // non-cryptographic jitter for reconnect staggering
	delay := h.cfg.RetryHintMinMS + rand.IntN(h.cfg.RetryHintMaxMS-h.cfg.RetryHintMinMS+1)
	msg := redis.XMessage{
		ID: "0-0", // not a real stream ID; the handler reads the special marker, not the ID
		Values: map[string]any{
			"event_type": "_retry_hint",
			"retry_ms":   strconv.Itoa(delay),
		},
	}
	_ = h.hub.Broadcast(ctx, msg)
}

// HandleStream is the gin handler for GET /api/v1/admin/events/stream.
func (h *Handler) HandleStream(c *gin.Context) {
	if h.shuttingDown.Load() {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "server shutting down, please retry",
		})
		return
	}

	w := c.Writer
	ctx := c.Request.Context()

	// SSE headers (Q14 layered defense: also set in nginx)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.log.Error(ctx, "admin sse: response writer is not a Flusher")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Parse Last-Event-ID for resumption (Q6: Redis Stream ID on wire)
	lastID := c.GetHeader("Last-Event-ID")
	isReconnect := lastID != ""
	if !isReconnect {
		lastID = "$"
	} else {
		observability.AdminSSEReconnectsTotal.Inc()
	}

	// Register client with hub BEFORE replay (Q8: subscribe-then-replay
	// so live events buffer into client.Send while replay runs)
	client := NewClient()
	registered := h.hub.Register(ctx, client)
	if !registered {
		h.log.Warn(ctx, "admin sse: hub unavailable, refusing connection")
		return
	}
	defer h.hub.Unregister(client)

	startedAt := time.Now()
	defer func() {
		observability.AdminSSEConnectionDurationSeconds.Observe(time.Since(startedAt).Seconds())
	}()

	// Replay historical events if Last-Event-ID provided
	if isReconnect {
		if err := h.replayHistory(ctx, w, flusher, lastID); err != nil {
			h.log.Warn(ctx, "admin sse replay failed",
				mlog.String("last_event_id", lastID),
				tag.Error(err))
			// Don't return — continue to live mode anyway
		}
	}

	// Writer loop: events + heartbeat (Q13)
	heartbeat := time.NewTicker(h.cfg.HeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			// Client disconnected
			return

		case msg, ok := <-client.Send:
			if !ok {
				// Hub closed our send chan — either we were dropped
				// (slow consumer) or hub is shutting down. Either way,
				// terminate the connection cleanly.
				return
			}
			h.writeMessage(w, flusher, msg)

		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
			observability.AdminSSEHeartbeatsSentTotal.Inc()
		}
	}
}

// replayHistory reads events from XRANGE (exclusive of lastID) and
// writes them to the SSE response. Handles the truncated case (Q8
// edge: Last-Event-ID before stream's earliest entry) by emitting
// a synthetic stream_truncated event.
func (h *Handler) replayHistory(ctx context.Context, w io.Writer, flusher http.Flusher, lastID string) error {
	msgs, err := h.client.XRangeN(ctx, h.cfg.StreamKey, "("+lastID, "+", h.cfg.ReplayMaxCount).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("xrange: %w", err)
	}

	if len(msgs) == 0 {
		// Check if lastID is older than the stream's first entry —
		// indicates trim/truncation
		first, err := h.client.XRangeN(ctx, h.cfg.StreamKey, "-", "+", 1).Result()
		if err == nil && len(first) > 0 && compareStreamIDs(lastID, first[0].ID) < 0 {
			observability.AdminSSEEventsTruncatedTotal.Inc()
			truncMsg := redis.XMessage{
				ID: "0-0",
				Values: map[string]any{
					"event_type": "stream_truncated",
					"data":       `{"reason":"last_event_id_too_old","action":"resuming_from_live"}`,
				},
			}
			h.writeMessage(w, flusher, truncMsg)
		}
		return nil
	}

	for _, m := range msgs {
		h.writeMessage(w, flusher, m)
	}
	return nil
}

// writeMessage serializes a Redis stream message into an SSE frame
// and flushes. Recognizes the internal _retry_hint marker (emits
// only the retry: line, no id/event/data) per Q15 graceful shutdown.
//
// Fprintf errors are intentionally ignored: an SSE writer that
// errors on write means the client has disconnected, which the
// outer handler loop already detects via ctx.Done() — surfacing
// the per-line error here would only add noise.
func (h *Handler) writeMessage(w io.Writer, flusher http.Flusher, msg redis.XMessage) {
	eventType, _ := msg.Values["event_type"].(string)

	// Special internal marker for graceful-shutdown retry hint
	if eventType == "_retry_hint" {
		if retryMS, ok := msg.Values["retry_ms"].(string); ok {
			_, _ = fmt.Fprintf(w, "retry: %s\n\n", retryMS)
			flusher.Flush()
		}
		return
	}

	_, _ = fmt.Fprintf(w, "id: %s\n", msg.ID)
	if eventType != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", eventType)
	}
	data, _ := msg.Values["data"].(string)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	// Track per-event-type sent counter + message-lag histogram
	if eventType != "" {
		observability.AdminSSEMessagesSentTotal.WithLabelValues(eventType).Inc()
	}
	if occurredAt, ok := msg.Values["occurred_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, occurredAt); err == nil {
			observability.AdminSSEMessageLagSeconds.Observe(time.Since(t).Seconds())
		}
	}
}

// compareStreamIDs compares two Redis Stream IDs (format "ms-seq").
// Returns -1, 0, or 1 like strings.Compare. Used to detect lastID-
// older-than-first-entry truncation.
//
// Falls back to lexicographic comparison if either ID isn't in the
// standard ms-seq format ("$" or "0-0" sentinels). For real Stream
// IDs this gives correct chronological ordering because both ms
// and seq are zero-padded equivalent... actually no, they're not
// padded, so we parse.
func compareStreamIDs(a, b string) int {
	am, as := splitStreamID(a)
	bm, bs := splitStreamID(b)
	if am != bm {
		if am < bm {
			return -1
		}
		return 1
	}
	if as < bs {
		return -1
	}
	if as > bs {
		return 1
	}
	return 0
}

func splitStreamID(id string) (int64, int64) {
	for i := 0; i < len(id); i++ {
		if id[i] == '-' {
			ms, _ := strconv.ParseInt(id[:i], 10, 64)
			seq, _ := strconv.ParseInt(id[i+1:], 10, 64)
			return ms, seq
		}
	}
	ms, _ := strconv.ParseInt(id, 10, 64)
	return ms, 0
}

func applyHandlerDefaults(cfg *HandlerConfig) {
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = DefaultHandlerHeartbeatInterval
	}
	if cfg.ReplayMaxCount == 0 {
		cfg.ReplayMaxCount = DefaultHandlerReplayMaxCount
	}
	if cfg.RetryHintMinMS == 0 {
		cfg.RetryHintMinMS = DefaultHandlerRetryHintMinMS
	}
	if cfg.RetryHintMaxMS == 0 {
		cfg.RetryHintMaxMS = DefaultHandlerRetryHintMaxMS
	}
}
