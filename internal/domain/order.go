package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrOrderNotFound = errors.New("order not found")

	// Invariant violations from NewOrder. Caller-actionable errors —
	// each maps to a malformed-input case the worker should DLQ.
	ErrInvalidUserID   = errors.New("order user_id must be positive")
	ErrInvalidEventID  = errors.New("order event_id must not be the zero UUID")
	ErrInvalidQuantity = errors.New("order quantity must be positive")
)

// IsMalformedOrderInput reports whether err originated from a NewOrder
// invariant violation (zero/negative user_id, zero UUID event_id, or
// non-positive quantity).
//
// The booking pipeline uses this to route deterministic-failure
// messages straight to the DLQ instead of cycling the per-message
// retry budget. A UserID=0 message will return ErrInvalidUserID on
// every redelivery — retrying it 3× wastes ~600ms of backoff and 2
// pointless tx-open attempts before the inevitable DLQ write. The
// classifier short-circuits to first-attempt DLQ.
//
// Distinct from "transient" errors (DB conn lost, sold-out conflict,
// duplicate-purchase race) where redelivery has a chance of producing
// a different outcome — those still go through the retry budget.
//
// Lives in domain because the predicate is a property of the error
// itself; the queue/infrastructure layer that consumes it is just
// reading what the domain already exposes.
func IsMalformedOrderInput(err error) bool {
	return errors.Is(err, ErrInvalidUserID) ||
		errors.Is(err, ErrInvalidEventID) ||
		errors.Is(err, ErrInvalidQuantity)
}

type OrderStatus string

const (
	OrderStatusConfirmed   OrderStatus = "confirmed"
	OrderStatusPending     OrderStatus = "pending"
	OrderStatusFailed      OrderStatus = "failed"
	OrderStatusCompensated OrderStatus = "compensated"
)

// Order is the domain aggregate. All fields are unexported; reads
// happen through accessor methods (Wild Workouts pattern, no Get
// prefix), writes happen through the NewOrder factory or the
// immutable WithStatus transition. Construction outside this package
// is impossible — callers cannot bypass the factory's invariants.
//
// Field types:
//   - id, eventID: uuid.UUID. Both are factory-generated (NewV7) or
//     received from boundaries; never DB-assigned. UUIDv7 is
//     time-prefixed so B-tree indexes still cluster (see
//     memory/uuid_v7_research.md for benchmark).
//   - userID: int. STAYS int because users are an external concept
//     (this service does not own the users table).
//   - createdAt: factory-assigned, NOT DB-assigned. The UUIDv7 already
//     encodes ms-precision creation time; we keep CreatedAt as a
//     full time.Time for human-friendly display via DTOs and for
//     business logic that compares times directly.
type Order struct {
	id        uuid.UUID
	eventID   uuid.UUID
	userID    int
	quantity  int
	status    OrderStatus
	createdAt time.Time
}

// NewOrder constructs a fresh pending order. Validates invariants at
// the domain boundary, then assigns a fresh UUIDv7 id and a
// time.Now() createdAt. The returned Order is fully complete — no
// repository "fills in" anything.
func NewOrder(userID int, eventID uuid.UUID, quantity int) (Order, error) {
	if userID <= 0 {
		return Order{}, ErrInvalidUserID
	}
	if eventID == uuid.Nil {
		return Order{}, ErrInvalidEventID
	}
	if quantity <= 0 {
		return Order{}, ErrInvalidQuantity
	}
	id, err := uuid.NewV7()
	if err != nil {
		// crypto/rand failure — vanishingly rare but not impossible
		// under entropy exhaustion / fuzz. Surface so callers can
		// retry or DLQ instead of producing a zero-UUID order.
		return Order{}, fmt.Errorf("generate order id: %w", err)
	}
	return Order{
		id:        id,
		userID:    userID,
		eventID:   eventID,
		quantity:  quantity,
		status:    OrderStatusPending,
		createdAt: time.Now(),
	}, nil
}

// ReconstructOrder rehydrates an Order from a persisted row. Skips
// the invariant validation in NewOrder because the row was already
// validated at insert time. Use ONLY from repository row-scanning
// code, never to "create" a new order. Future refactor: move into
// internal/infrastructure/persistence/postgres so the visibility
// matches the contract; for now the comment-only contract holds
// because all postgres scan code is the only caller.
func ReconstructOrder(id uuid.UUID, userID int, eventID uuid.UUID, quantity int, status OrderStatus, createdAt time.Time) Order {
	return Order{
		id:        id,
		userID:    userID,
		eventID:   eventID,
		quantity:  quantity,
		status:    status,
		createdAt: createdAt,
	}
}

// WithStatus returns a copy of the order with the given status.
// Immutable transition — the receiver is untouched, so concurrent
// reads of the same Order value are safe.
func (o Order) WithStatus(status OrderStatus) Order {
	o.status = status
	return o
}

// Accessors — read-only views on the unexported fields. Wild Workouts
// pattern (no "Get" prefix), aligned with Go stdlib (time.Time.Hour()
// etc.).
func (o Order) ID() uuid.UUID         { return o.id }
func (o Order) EventID() uuid.UUID    { return o.eventID }
func (o Order) UserID() int           { return o.userID }
func (o Order) Quantity() int         { return o.quantity }
func (o Order) Status() OrderStatus   { return o.status }
func (o Order) CreatedAt() time.Time  { return o.createdAt }

//go:generate mockgen -source=order.go -destination=../mocks/order_repository_mock.go -package=mocks
type OrderRepository interface {
	// Create persists the order and returns it back unchanged. The
	// caller's input already has its UUID + CreatedAt set by the
	// factory, so the repo no longer "fills in" anything — Create's
	// signature still returns (Order, error) for API consistency
	// with prior code that needed the populated value.
	Create(ctx context.Context, order Order) (Order, error)

	// ListOrders returns a page of orders by value. Empty result is
	// a nil slice (not an error).
	ListOrders(ctx context.Context, limit, offset int, status *OrderStatus) ([]Order, int, error)

	// GetByID returns the order by id. ErrOrderNotFound when no row
	// matches; any other error is wrapped with the query context.
	GetByID(ctx context.Context, id uuid.UUID) (Order, error)

	UpdateStatus(ctx context.Context, id uuid.UUID, status OrderStatus) error
}
