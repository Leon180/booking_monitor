package application

import (
	"context"
	"errors"
)

// ErrInvalidPaymentEvent is returned by PaymentService.ProcessOrder
// when the OrderCreatedEvent payload fails input validation
// (zero/negative OrderID, negative Amount, etc.). Consumers MUST
// treat this as a dead-letter condition: log, publish to DLQ if
// configured, commit the offset, and move on. NEVER a retryable error.
//
// Lives in application (not domain) because it's the application-
// service's contract with its caller (the Kafka consumer). The
// classification "this event will never become valid" is a use-case
// concern, parallel to `domain.IsMalformedOrderInput` for the
// booking-stream worker.
var ErrInvalidPaymentEvent = errors.New("invalid payment event")

//go:generate mockgen -source=payment_service.go -destination=../mocks/payment_service_mock.go -package=mocks

// PaymentService defines the application-layer port for the payment
// pipeline (Kafka order.created consumer → gateway.Charge → outbox
// order.failed on failure). Implementation lives in
// internal/application/payment/service.go.
//
// Why application (not domain): the input is `*OrderCreatedEvent`,
// a wire-format DTO that lives in application. A domain-side
// interface couldn't reference the DTO without inverting the
// dependency rule. Mirrors the same reasoning that put OrderQueue
// in application during PR #37.
//
// `domain.PaymentGateway` (the external-integration port for Stripe /
// MockGateway) stays in domain — that one IS a true domain port,
// no DTOs leak through it.
type PaymentService interface {
	ProcessOrder(ctx context.Context, event *OrderCreatedEvent) error
}
