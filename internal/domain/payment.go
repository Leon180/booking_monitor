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
	EventID   string    `json:"event_id"`
	OrderID   int       `json:"order_id"`
	UserID    int       `json:"user_id"`
	TicketID  int       `json:"ticket_id"`
	Amount    float64   `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}
