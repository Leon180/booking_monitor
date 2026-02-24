package domain

import (
	"context"
	"time"
)

// PaymentGateway defines the interface for external payment processing.
type PaymentGateway interface {
	Charge(ctx context.Context, orderID int, amount float64) error
}

// PaymentService defines the application service for processing payments.
type PaymentService interface {
	ProcessOrder(ctx context.Context, event *OrderCreatedEvent) error
}

// OrderCreatedEvent represents the structure of the event consumed from Kafka.
// It mirrors the event published by the ticket service.
type OrderCreatedEvent struct {
	OrderID   int       `json:"id"`
	Status    string    `json:"status"`
	UserID    int       `json:"user_id"`
	EventID   int       `json:"event_id"`
	Quantity  int       `json:"quantity"`
	Amount    float64   `json:"amount"` // Deserialize amount for gateway charge
	CreatedAt time.Time `json:"created_at"`
}

// OrderFailedEvent represents the structure of the event published when a payment fails.
// It is consumed by the booking domain to trigger a compensating transaction (Saga).
type OrderFailedEvent struct {
	EventID  int       `json:"event_id"`
	OrderID  int       `json:"order_id"`
	UserID   int       `json:"user_id"`
	Quantity int       `json:"quantity"`
	FailedAt time.Time `json:"failed_at"`
	Reason   string    `json:"reason"`
}
