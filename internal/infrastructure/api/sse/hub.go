// Package sse hosts the admin event SSE handler and supporting hub /
// subscriber goroutines. See `docs/design/admin_event_streaming.md` for
// the full architecture (Q7-Q15).
package sse

import (
	"context"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
)

// ClientSendBufferCapacity caps each connected client's outbound
// channel. When full, the hub drops the client (Q10 design — force
// reconnect rather than silently drop messages).
const ClientSendBufferCapacity = 100

// Client represents one connected SSE client managed by the hub.
// Created by the handler at request entry, registered via
// hub.Register, and unregistered in handler defer.
type Client struct {
	ID   string
	Send chan redis.XMessage
}

// NewClient mints a Client with a fresh UUIDv7 id and bounded send
// channel.
func NewClient() *Client {
	return &Client{
		ID:   uuid.Must(uuid.NewV7()).String(),
		Send: make(chan redis.XMessage, ClientSendBufferCapacity),
	}
}

// Hub fan-outs admin events to all connected SSE clients. Uses the
// channel-based Run-loop pattern (Q9 design): single goroutine owns
// the clients map, no shared-state mutex.
//
// Lifecycle:
//   - NewHub() returns an unstarted Hub.
//   - Spawn Run(ctx) in a goroutine; it processes register / unregister
//     / broadcast until ctx is cancelled.
//   - On ctx.Done(), Run closes every client.Send (writer goroutines
//     exit naturally) and closes Stopped() to signal handlers their
//     pending unregister can be skipped.
type Hub struct {
	register   chan *Client
	unregister chan *Client
	broadcast  chan redis.XMessage

	// stopped is closed by Run() on exit so the SSE handler's defer
	// can short-circuit a futile unregister send (Q15 graceful drain).
	stopped chan struct{}

	log *mlog.Logger
}

// NewHub constructs an unstarted Hub.
//
// register/unregister have small buffers so connect/disconnect bursts
// don't make handlers block waiting for the hub loop. Broadcast is
// also buffered so the Redis subscriber goroutine can push events
// without waiting on a slow client.
func NewHub(logger *mlog.Logger) *Hub {
	return &Hub{
		register:   make(chan *Client, 32),
		unregister: make(chan *Client, 32),
		broadcast:  make(chan redis.XMessage, 256),
		stopped:    make(chan struct{}),
		log:        logger.With(mlog.String("component", "admin_sse_hub")),
	}
}

// Register enqueues the client for registration. Should be called from
// the SSE handler at request entry. Returns false if the hub is
// already stopped or the caller's ctx is cancelled.
//
// The fast-path stopped check is required because all three of
// register / unregister / broadcast are buffered — without it, a
// late Register after hub shutdown could win the select's send
// branch (buffer not full) instead of the stopped branch.
func (h *Hub) Register(ctx context.Context, c *Client) bool {
	select {
	case <-h.stopped:
		return false
	default:
	}
	select {
	case h.register <- c:
		return true
	case <-ctx.Done():
		return false
	case <-h.stopped:
		return false
	}
}

// Unregister enqueues a client for removal. Should be called in the
// SSE handler defer. Non-blocking on hub shutdown (Stopped() signal).
func (h *Hub) Unregister(c *Client) {
	select {
	case <-h.stopped:
		return
	default:
	}
	select {
	case h.unregister <- c:
	case <-h.stopped:
		// Hub gone — skip; c.Send already closed by hub shutdown
	}
}

// Broadcast enqueues a message for fan-out. Called by the Redis
// subscriber goroutine. Returns false if the hub stops or ctx
// cancels before the message is enqueued.
func (h *Hub) Broadcast(ctx context.Context, msg redis.XMessage) bool {
	select {
	case <-h.stopped:
		return false
	default:
	}
	select {
	case h.broadcast <- msg:
		return true
	case <-ctx.Done():
		return false
	case <-h.stopped:
		return false
	}
}

// Stopped returns a channel closed when Run() exits.
func (h *Hub) Stopped() <-chan struct{} { return h.stopped }

// Run is the hub event loop. Owns the clients map exclusively. Exits
// when ctx is cancelled, closing every client.Send on exit.
func (h *Hub) Run(ctx context.Context) {
	defer close(h.stopped)

	clients := make(map[*Client]struct{})
	defer func() {
		// Final cleanup: close every active client.Send so writer
		// goroutines exit. Handlers' defer Unregister becomes a no-op
		// (Stopped() short-circuit).
		for c := range clients {
			close(c.Send)
		}
		observability.AdminSSEActiveConnections.Set(0)
	}()

	h.log.Info(ctx, "admin sse hub starting")

	for {
		select {
		case <-ctx.Done():
			h.log.Info(context.Background(), "admin sse hub stopping",
				mlog.Int("active_clients", len(clients)))
			return

		case c := <-h.register:
			clients[c] = struct{}{}
			observability.AdminSSEActiveConnections.Inc()

		case c := <-h.unregister:
			if _, ok := clients[c]; ok {
				delete(clients, c)
				close(c.Send)
				observability.AdminSSEActiveConnections.Dec()
			}

		case msg := <-h.broadcast:
			// Q10 backpressure: slow client → drop client + force
			// reconnect. EventSource auto-reconnects with Last-Event-ID,
			// Q8 XRANGE replay backfills the gap.
			for c := range clients {
				select {
				case c.Send <- msg:
					// delivered
				default:
					// c.Send full → drop client
					delete(clients, c)
					close(c.Send)
					observability.AdminSSEActiveConnections.Dec()
					observability.AdminSSEClientsDroppedTotal.WithLabelValues("slow_consumer").Inc()
					h.log.Warn(context.Background(), "admin sse hub dropped slow client",
						mlog.String("client_id", c.ID))
				}
			}
		}
	}
}
