package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrEventNotFound     = errors.New("event not found")
	ErrSoldOut           = errors.New("event sold out")
	ErrUserAlreadyBought = errors.New("user already bought ticket")

	// Invariant violations from NewEvent.
	ErrInvalidEventName    = errors.New("event name must not be empty")
	ErrInvalidTotalTickets = errors.New("event total_tickets must be positive")
)

const (
	// EventTypeOrderFailed is the canonical outbox event_type value
	// for saga-compensation triggers. Hardcoding the string at call
	// sites is a typo waiting to happen — use the constant and the
	// `NewOrderFailedOutbox` factory below.
	//
	// D7 (2026-05-08) deleted the sibling `EventTypeOrderCreated` /
	// `NewOrderCreatedOutbox` along with the legacy A4 auto-charge
	// path. `order.failed` is the only outbox event type now;
	// emitters are: D5 webhook (`payment_failed`), D6 expiry sweeper
	// (`expired`), and recon's `failOrder` (rare; tagged via
	// `Reason="recon: ..."`).
	EventTypeOrderFailed = "order.failed"

	// OutboxStatusPending is the initial status assigned by every
	// New*Outbox factory; the OutboxRelay flips rows to PROCESSED
	// after publish.
	OutboxStatusPending = "PENDING"
)

// Event is the domain aggregate. Field names are unexported; reads
// via accessor methods, writes via NewEvent factory or Deduct
// transition. See order.go for the full rationale.
type Event struct {
	id               uuid.UUID
	name             string
	totalTickets     int
	availableTickets int
	version          int
}

// NewEvent constructs a fresh Event with the canonical "available =
// total at creation" invariant + non-empty name + positive
// total_tickets. Generates a UUIDv7 id at construction.
func NewEvent(name string, totalTickets int) (Event, error) {
	if strings.TrimSpace(name) == "" {
		return Event{}, ErrInvalidEventName
	}
	if totalTickets <= 0 {
		return Event{}, ErrInvalidTotalTickets
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, fmt.Errorf("generate event id: %w", err)
	}
	return Event{
		id:               id,
		name:             name,
		totalTickets:     totalTickets,
		availableTickets: totalTickets,
		version:          0,
	}, nil
}

// ReconstructEvent rehydrates an Event from a persisted row. Skips
// invariant validation; postgres scan code is the only intended caller.
func ReconstructEvent(id uuid.UUID, name string, totalTickets, availableTickets, version int) Event {
	return Event{
		id:               id,
		name:             name,
		totalTickets:     totalTickets,
		availableTickets: availableTickets,
		version:          version,
	}
}

// Accessors — Wild Workouts pattern (no Get prefix).
func (e Event) ID() uuid.UUID      { return e.id }
func (e Event) Name() string       { return e.name }
func (e Event) TotalTickets() int  { return e.totalTickets }
func (e Event) AvailableTickets() int { return e.availableTickets }
func (e Event) Version() int       { return e.version }

// Deduct returns a new Event with AvailableTickets decremented by
// quantity, or an error. Immutable: the receiver is NOT mutated.
func (e Event) Deduct(quantity int) (Event, error) {
	if quantity < 0 {
		return Event{}, errors.New("invalid quantity")
	}
	if e.availableTickets < quantity {
		return Event{}, ErrSoldOut
	}
	next := e
	next.availableTickets -= quantity
	return next, nil
}

//go:generate mockgen -source=event.go -destination=../mocks/event_repository_mock.go -package=mocks
type EventRepository interface {
	// Create persists the event. Factory has assigned id + version, so
	// the input is value-in; the returned Event mirrors the persisted
	// row (currently unchanged from input, but the signature leaves
	// room for future server-side defaults — symmetric with
	// OrderRepository.Create).
	Create(ctx context.Context, event Event) (Event, error)
	// GetByID is a plain read with no row lock. Returns a value Event;
	// Event{}+ErrEventNotFound when no row matches.
	GetByID(ctx context.Context, id uuid.UUID) (Event, error)
	// GetByIDForUpdate takes a FOR UPDATE row lock; MUST be called
	// inside a UoW-managed transaction.
	GetByIDForUpdate(ctx context.Context, id uuid.UUID) (Event, error)
	Update(ctx context.Context, event Event) error
	// Deprecated: D4.1 follow-up. Inventory now lives on
	// event_ticket_types.available_tickets — use
	// TicketTypeRepository.DecrementTicket / IncrementTicket instead.
	// `events.available_tickets` is frozen post-D4.1 (initialised at
	// CreateEvent then never written) and a follow-up migration removes
	// the column. These methods are retained for backward-compat with
	// old tests and any out-of-tree callers; production hot paths
	// (worker + saga compensator) no longer call them.
	DecrementTicket(ctx context.Context, eventID uuid.UUID, quantity int) error
	// Deprecated: see DecrementTicket above.
	IncrementTicket(ctx context.Context, eventID uuid.UUID, quantity int) error
	Delete(ctx context.Context, id uuid.UUID) error
	// ListAvailable returns events with `available_tickets > 0`,
	// ordered newest-first. Used by app-startup inventory rehydrate
	// (cache.RehydrateInventory) to repopulate Redis from DB after
	// FLUSHALL / Redis restart / fresh deploy. The result set is the
	// "active" events whose Redis qty keys must exist for the booking
	// hot path to function — sold-out events are excluded because Lua
	// deduct correctly returns sold-out for missing keys (DECRBY → -N
	// → revert path → return -1).
	ListAvailable(ctx context.Context) ([]Event, error)
}

// OutboxEvent is the outbox-row aggregate. Field names unexported.
// ID is factory-assigned (UUIDv7), not DB-assigned. CreatedAt is
// factory-assigned (`time.Now()`) at construction; the postgres
// schema's `events_outbox.created_at DEFAULT NOW()` is a defensive
// fallback for direct-SQL inserts that bypass the factory.
//
// CreatedAt is load-bearing for the saga compensation latency
// histogram (PR-D12.4): it's the histogram's start time, threaded
// from here through `EventPublisher.PublishOutboxEvent` →
// `kafka.Message.Time` → SagaConsumer's `time.Since(msg.Time)`. A
// regression that loses CreatedAt anywhere in this chain produces
// zero-value time → astronomical histogram observations all
// landing in `+Inf` → broken percentile calculations. The
// integration test at
// `test/integration/postgres/outbox_kafka_time_test.go` is the
// regression tripwire.
type OutboxEvent struct {
	id          uuid.UUID
	eventType   string
	payload     []byte // JSON
	status      string
	createdAt   time.Time
	processedAt *time.Time
}

// ReconstructOutboxEvent rehydrates from a persisted row.
func ReconstructOutboxEvent(id uuid.UUID, eventType string, payload []byte, status string, createdAt time.Time, processedAt *time.Time) OutboxEvent {
	return OutboxEvent{
		id:          id,
		eventType:   eventType,
		payload:     payload,
		status:      status,
		createdAt:   createdAt,
		processedAt: processedAt,
	}
}

// Accessors.
func (e OutboxEvent) ID() uuid.UUID           { return e.id }
func (e OutboxEvent) EventType() string       { return e.eventType }
func (e OutboxEvent) Payload() []byte         { return e.payload }
func (e OutboxEvent) Status() string          { return e.status }
func (e OutboxEvent) CreatedAt() time.Time    { return e.createdAt }
func (e OutboxEvent) ProcessedAt() *time.Time { return e.processedAt }

// NewOrderFailedOutbox constructs a pending outbox event for an
// `order.failed` payload (saga compensation trigger). CreatedAt
// is set to `time.Now().UTC()` so the downstream histogram
// measures from this moment. The `Create` repo method explicitly
// writes this value into `events_outbox.created_at` (see
// `internal/infrastructure/persistence/postgres/repositories.go`'s
// outbox INSERT — the `$5` parameter); the schema's
// `DEFAULT NOW()` is preserved as a defensive fallback for
// direct-SQL bypasses that skip the factory.
//
// SCHEMA-DEBT NOTE: `events_outbox.created_at` is `TIMESTAMP`
// (timezone-naive) per migration 000003 + 000008. The factory
// path (UTC time → explicit INSERT → UTC read-back) is correct.
// But the DB-side `DEFAULT NOW()` fallback fires in server-local
// time on non-UTC DB servers, which lib/pq interprets as UTC+0
// — silently embedding a wrong timestamp. Migration 000008
// already documents this debt as a separate cleanup. Future
// migration: `ALTER TABLE events_outbox ALTER COLUMN created_at
// TYPE TIMESTAMPTZ USING created_at AT TIME ZONE 'UTC'`.
func NewOrderFailedOutbox(payload []byte) (OutboxEvent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("generate outbox event id: %w", err)
	}
	return OutboxEvent{
		id:        id,
		eventType: EventTypeOrderFailed,
		payload:   payload,
		status:    OutboxStatusPending,
		createdAt: time.Now().UTC(),
	}, nil
}

type OutboxRepository interface {
	Create(ctx context.Context, event OutboxEvent) (OutboxEvent, error)
	ListPending(ctx context.Context, limit int) ([]OutboxEvent, error)
	MarkProcessed(ctx context.Context, id uuid.UUID) error
}
