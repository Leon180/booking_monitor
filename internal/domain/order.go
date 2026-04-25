package domain

import (
	"context"
	"errors"
	"time"
)

var (
	ErrOrderNotFound = errors.New("order not found")

	// Invariant violations from NewOrder. Caller-actionable errors —
	// each maps to a malformed-input case the worker should DLQ.
	ErrInvalidUserID   = errors.New("order user_id must be positive")
	ErrInvalidEventID  = errors.New("order event_id must be positive")
	ErrInvalidQuantity = errors.New("order quantity must be positive")
)

type OrderStatus string

const (
	OrderStatusConfirmed   OrderStatus = "confirmed"
	OrderStatusPending     OrderStatus = "pending"
	OrderStatusFailed      OrderStatus = "failed"
	OrderStatusCompensated OrderStatus = "compensated"
)

type Order struct {
	ID        int         `json:"id"`
	EventID   int         `json:"event_id"`
	UserID    int         `json:"user_id"`
	Quantity  int         `json:"quantity"`
	Status    OrderStatus `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
}

// NewOrder constructs a fresh pending order. Enforces invariants at
// the domain boundary so callers can't ship a zero-quantity order or
// skip the Pending-first lifecycle. Returns an Order value (not a
// pointer) — the project's coding standard prefers immutable
// value-typed entities; callers that need a pointer can take its
// address. ID + CreatedAt are set by the repository on insert.
func NewOrder(userID, eventID, quantity int) (Order, error) {
	if userID <= 0 {
		return Order{}, ErrInvalidUserID
	}
	if eventID <= 0 {
		return Order{}, ErrInvalidEventID
	}
	if quantity <= 0 {
		return Order{}, ErrInvalidQuantity
	}
	return Order{
		UserID:   userID,
		EventID:  eventID,
		Quantity: quantity,
		Status:   OrderStatusPending,
		// CreatedAt is left zero here so the repository can set it
		// from RETURNING created_at — that's the source of truth.
		// Worker / API code paths that need a CreatedAt before insert
		// should set it explicitly via a future field-setter; for now
		// every NewOrder caller hands the result to a repo that fills
		// it in.
	}, nil
}

// ReconstructOrder rehydrates an Order from a persisted row. Skips
// the invariant validation in NewOrder because the row was already
// validated at insert time and persisted state is trusted. Use ONLY
// from repository row-scanning code, never to "create" a new order.
func ReconstructOrder(id, userID, eventID, quantity int, status OrderStatus, createdAt time.Time) Order {
	return Order{
		ID:        id,
		UserID:    userID,
		EventID:   eventID,
		Quantity:  quantity,
		Status:    status,
		CreatedAt: createdAt,
	}
}

// WithStatus returns a copy of the order with the given status.
// Immutable transition method — the receiver is untouched, so
// concurrent reads of the same Order value are safe. Callers that
// also need to persist the new status should pass the returned value
// to the repository's UpdateStatus method.
func (o Order) WithStatus(status OrderStatus) Order {
	o.Status = status
	return o
}

//go:generate mockgen -source=order.go -destination=../mocks/order_repository_mock.go -package=mocks
type OrderRepository interface {
	// Create persists the order and returns a new Order value with the
	// repo-assigned ID + CreatedAt populated. The input order's
	// pre-insert state (UserID/EventID/Quantity/Status) is preserved.
	// Value-in / value-out: the caller's input is never mutated, and
	// the returned Order reflects the persisted truth.
	Create(ctx context.Context, order Order) (Order, error)

	// ListOrders returns a page of orders by value. Empty result is a
	// nil slice (not an error).
	ListOrders(ctx context.Context, limit, offset int, status *OrderStatus) ([]Order, int, error)

	// GetByID returns the order by id. ErrOrderNotFound when no row
	// matches; any other error is wrapped with the query context.
	GetByID(ctx context.Context, id int) (Order, error)

	UpdateStatus(ctx context.Context, id int, status OrderStatus) error
}
