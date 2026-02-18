package domain

import (
	"context"
	"time"
)

type OrderStatus string

const (
	OrderStatusConfirmed OrderStatus = "confirmed"
	OrderStatusPending   OrderStatus = "pending"
	OrderStatusFailed    OrderStatus = "failed"
)

type Order struct {
	ID        int         `json:"id"`
	EventID   int         `json:"event_id"`
	UserID    int         `json:"user_id"`
	Quantity  int         `json:"quantity"`
	Status    OrderStatus `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
}

type OrderRepository interface {
	Create(ctx context.Context, order *Order) error
	ListOrders(ctx context.Context, limit, offset int, status *OrderStatus) ([]*Order, int, error)
}
