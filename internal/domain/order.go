package domain

import (
	"context"
	"errors"
	"time"
)

var ErrOrderNotFound = errors.New("order not found")

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

//go:generate mockgen -source=order.go -destination=../mocks/order_repository_mock.go -package=mocks
type OrderRepository interface {
	Create(ctx context.Context, order *Order) error
	ListOrders(ctx context.Context, limit, offset int, status *OrderStatus) ([]*Order, int, error)
	GetByID(ctx context.Context, id int) (*Order, error)
	UpdateStatus(ctx context.Context, id int, status OrderStatus) error
}
