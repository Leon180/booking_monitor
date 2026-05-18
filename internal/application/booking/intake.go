package booking

import (
	"context"

	"github.com/google/uuid"
)

// IntakeMessage is the wire contract for a Stage 5 booking
// reservation message published to a durable queue (Kafka).
//
// Defined in the application/booking package (not the infrastructure
// messaging package) so application code can reference the type
// without depending on the concrete transport. The messaging package
// imports this type and provides the Kafka-backed publisher.
//
// JSON encoded with snake_case keys to match the orders:stream
// payload shape used by Stages 3-4 — keeps the consumer-side message
// processor adaptation minimal (just point at a different source).
type IntakeMessage struct {
	OrderID       uuid.UUID `json:"order_id"`
	UserID        int       `json:"user_id"`
	EventID       uuid.UUID `json:"event_id"`
	TicketTypeID  uuid.UUID `json:"ticket_type_id"`
	Quantity      int       `json:"quantity"`
	ReservedUntil int64     `json:"reserved_until_unix"`
	AmountCents   int64     `json:"amount_cents"`
	Currency      string    `json:"currency"`
}

// IntakePublisher is the Stage 5 contract for publishing booking
// reservations to a durable queue. Stage 5's BookingService calls
// PublishIntake after the atomic Lua deduct; the concrete
// implementation (messaging.IntakePublisher) publishes to Kafka
// with acks=all and blocks until the broker confirms write to all
// in-sync replicas.
//
// Returning an error means the publish failed. The caller (Stage 5
// BookingService) MUST treat this as a failure and call
// RevertInventory on the already-deducted Redis qty before
// returning the error to the client.
type IntakePublisher interface {
	PublishIntake(ctx context.Context, msg IntakeMessage) error
	// Close flushes in-flight messages and releases broker connections.
	// Must be called during graceful shutdown before the process exits.
	Close() error
}

// IntakeConsumer is the Stage 5 contract for consuming booking reservation
// messages from the durable queue and persisting them to PG. Symmetric
// counterpart to IntakePublisher — both live here so application code
// and cmd wiring never import the concrete messaging package.
type IntakeConsumer interface {
	// Start blocks until ctx is cancelled, draining the intake topic.
	Start(ctx context.Context) error
	// Ping verifies broker reachability. Called from fx OnStart with the
	// startup context so the process fails fast if the broker is down.
	Ping(ctx context.Context) error
	// Close shuts down the reader and releases broker connections.
	Close() error
}
