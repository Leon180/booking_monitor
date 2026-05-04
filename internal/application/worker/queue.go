package worker

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// QueuedBookingMessage is the application-layer DTO for an order
// message in transit through whatever queue infrastructure backs the
// worker (currently Redis Streams; could be Kafka / NATS / SQS in
// future). It carries:
//
//   - the booking payload that the worker will turn into a
//     domain.Order via NewOrder (UserID, EventID, Quantity)
//   - transport-assigned metadata the queue impl needs back at ACK time
//     (MessageID — opaque to the worker; the queue interprets it)
//
// Lives in application (not domain, not cache) because:
//   - it has NO business invariants enforced; NewOrder downstream does
//     the actual validation. So it's not a domain entity, it's a DTO.
//   - the MessageID's shape is transport-specific (Redis Stream IDs are
//     "<timestamp>-<seq>"; Kafka offsets are int64; etc.) — leaking
//     that through domain would tie the domain to today's transport.
//   - both producers (Lua deduct → Redis Stream) and consumers
//     (worker → message_processor) live at or below application; no
//     domain code ever touches this type.
//
// The PR-37 relocation of this type from `domain.OrderMessage` was
// driven by exactly that reasoning: the old comment on the ID field
// ("Redis stream message ID") was a self-aware admission that the
// type didn't belong in domain.
type QueuedBookingMessage struct {
	// MessageID is the transport-assigned identifier the queue impl
	// uses for ACK / PEL / DLQ bookkeeping. Opaque to the worker —
	// only the queue itself interprets it. For Redis Streams this is
	// the "<timestamp>-<seq>" form; for Kafka it would be a string
	// rendering of (topic, partition, offset).
	MessageID string

	// OrderID is the caller-minted UUIDv7 from the API boundary. Flows
	// end-to-end so the id the client received at HTTP 202 is the
	// same id the worker writes to DB, the same id outbox/payment/
	// saga reference, and — critically — the same id reused across
	// PEL retries. Pre-PR-47 the worker minted its own uuid per
	// message and PEL retries diverged from what the client held.
	OrderID  uuid.UUID
	UserID   int       // External user reference (this service does not own users)
	EventID  uuid.UUID // FK to events.id
	Quantity int

	// ReservedUntil is the Pattern A reservation TTL — the timestamp
	// past which the D6 expiry sweeper will mark the order Expired and
	// revert Redis inventory. Computed by BookingService as `time.Now()
	// + window`, threaded through Lua deduct as a unix-seconds integer,
	// re-parsed back to time.Time at the queue boundary so the worker
	// gets a strongly-typed value to write into orders.reserved_until.
	//
	// Always UTC, always non-zero (the producer rejects zero values
	// before the Lua script runs and parseMessage rejects them at the
	// stream boundary). Worker code can rely on `!IsZero() && After(now)`
	// at message-receive time, modulo Kafka-style delivery-delay
	// pathologies (a multi-minute redelivery on a 15-minute reservation
	// is theoretically observable but not actually a correctness
	// problem — the row would just be inserted with a near-past TTL
	// and the D6 sweeper would resolve it on the next tick).
	ReservedUntil time.Time

	// TicketTypeID + AmountCents + Currency — D4.1 KKTIX-aligned
	// 票種 + price snapshot. BookingService looked up the chosen
	// ticket_type, derived the event_id + price_cents + currency,
	// computed amount_cents = price_cents × quantity, and frozen
	// these values onto the stream message so the worker doesn't
	// re-query (the worker is the async path; querying twice would
	// race against admin price edits).
	TicketTypeID uuid.UUID
	AmountCents  int64
	Currency     string
}

//go:generate mockgen -source=queue.go -destination=../mocks/queue_mock.go -package=mocks

// OrderQueue is the application-side port for the order-stream
// consumer. The implementation (`infrastructure/cache/redis_queue.go`)
// owns the stream/group/DLQ machinery; this interface is the contract
// application services depend on.
//
// Why application, not domain: the queue is a transport abstraction
// (broker / stream / pubsub), not a domain concept. Compare to
// `domain.OrderRepository` which IS a domain port (defines how the
// Order aggregate is persisted) — that legitimately belongs in domain.
// "Where messages buffer between processes" is application-layer.
type OrderQueue interface {
	// EnsureGroup is idempotent group + stream creation. Run once at
	// startup before Subscribe; safe to call repeatedly.
	EnsureGroup(ctx context.Context) error

	// Subscribe is the long-running consumer loop. Blocks until ctx
	// is cancelled or a persistent failure threshold trips. Returns
	// nil on clean ctx-cancel shutdown; wraps the underlying error
	// otherwise. handler is invoked per-message with the parsed
	// QueuedBookingMessage; the queue manages retry / ACK / DLQ
	// based on what handler returns and the injected retry policy.
	Subscribe(ctx context.Context, handler func(ctx context.Context, msg *QueuedBookingMessage) error) error
}
