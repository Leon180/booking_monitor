package domain

import (
	"context"
	"errors"
	"time"
)

var (
	ErrEventNotFound     = errors.New("event not found")
	ErrSoldOut           = errors.New("event sold out")
	ErrUserAlreadyBought = errors.New("user already bought ticket")
)

const (
	EventTypeOrderFailed = "order.failed"
	OutboxStatusPending  = "PENDING"
)

type Event struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	TotalTickets     int    `json:"total_tickets"`
	AvailableTickets int    `json:"available_tickets"`
	Version          int    `json:"version"` // Added Version
}

// Deduct returns a new *Event with AvailableTickets decremented by
// quantity, or an error. It is immutable: the receiver is NOT mutated.
// This follows the project's global "create new objects, never mutate"
// coding style rule and makes concurrent reads of an Event safe.
func (e *Event) Deduct(quantity int) (*Event, error) {
	if quantity < 0 {
		return nil, errors.New("invalid quantity")
	}
	if e.AvailableTickets < quantity {
		return nil, ErrSoldOut
	}
	next := *e
	next.AvailableTickets -= quantity
	return &next, nil
}

//go:generate mockgen -source=event.go -destination=../mocks/event_repository_mock.go -package=mocks
type EventRepository interface {
	Create(ctx context.Context, event *Event) error
	// GetByID is a plain read with no row lock. Safe outside a transaction.
	GetByID(ctx context.Context, id int) (*Event, error)
	// GetByIDForUpdate takes a FOR UPDATE row lock and MUST be called
	// inside a UoW-managed transaction. See persistence/postgres for the
	// rationale.
	GetByIDForUpdate(ctx context.Context, id int) (*Event, error)
	Update(ctx context.Context, event *Event) error
	DecrementTicket(ctx context.Context, eventID, quantity int) error
	IncrementTicket(ctx context.Context, eventID, quantity int) error
	// Delete removes an event. Used by EventService.CreateEvent as a
	// compensating action when the dual-write to the Redis hot-path
	// inventory fails after the DB row has been committed.
	Delete(ctx context.Context, id int) error
}

type OutboxEvent struct {
	ID          int
	EventType   string
	Payload     []byte // JSON
	Status      string
	ProcessedAt *time.Time
}

type OutboxRepository interface {
	Create(ctx context.Context, event *OutboxEvent) error
	ListPending(ctx context.Context, limit int) ([]*OutboxEvent, error)
	MarkProcessed(ctx context.Context, id int) error
}
