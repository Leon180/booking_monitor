package domain

import (
	"context"
	"errors"
)

// ErrInvalidPaymentEvent is returned by PaymentService.ProcessOrder when the
// OrderCreatedEvent payload fails input validation (zero/negative OrderID,
// negative Amount, etc.). Consumers MUST treat this as a dead-letter
// condition: log, publish to DLQ if configured, commit the offset, and move
// on. It is NEVER a retryable error.
var ErrInvalidPaymentEvent = errors.New("invalid payment event")

// PaymentGateway defines the interface for external payment processing.
type PaymentGateway interface {
	Charge(ctx context.Context, orderID int, amount float64) error
}

// PaymentService defines the application service for processing payments.
type PaymentService interface {
	ProcessOrder(ctx context.Context, event *OrderCreatedEvent) error
}

// OrderCreatedEvent and OrderFailedEvent moved to order_events.go in
// PR 32 — they are wire-format messaging payloads, conceptually
// distinct from the payment domain interfaces above. Their producer-
// side mappers (NewOrderCreatedEvent / NewOrderFailedEvent) live with
// the types so the domain↔wire seam is in one file.
