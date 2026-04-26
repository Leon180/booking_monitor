package domain

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrInvalidPaymentEvent is returned by PaymentService.ProcessOrder when the
// OrderCreatedEvent payload fails input validation (zero/negative OrderID,
// negative Amount, etc.). Consumers MUST treat this as a dead-letter
// condition: log, publish to DLQ if configured, commit the offset, and move
// on. It is NEVER a retryable error.
var ErrInvalidPaymentEvent = errors.New("invalid payment event")

//go:generate mockgen -source=payment.go -destination=../mocks/payment_mock.go -package=mocks

// PaymentGateway defines the interface for external payment processing.
//
// IDEMPOTENCY CONTRACT (load-bearing — payment service relies on it):
//
// Implementations MUST treat orderID as an idempotency key. Repeat
// Charge calls with the same orderID MUST return the result of the
// FIRST call — they MUST NOT initiate a second charge against the
// payment provider.
//
// Why this contract is required, not optional:
//
//   PaymentService.ProcessOrder runs at-least-once (Kafka redelivery
//   on failure). The ordering Charge → DB UpdateStatus is a two-phase
//   operation across distinct infrastructures and CANNOT be wrapped
//   in a tx. Two race windows produce duplicate Charge calls on retry:
//
//     a) Charge succeeds, then UpdateStatus(Confirmed) fails. Kafka
//        redelivers; the idempotency check (Status==Pending) sees
//        the unchanged state and re-enters the Charge path.
//     b) Charge fails, then the saga uow.Do (UpdateStatus(Failed) +
//        outbox order.failed) fails. Kafka redelivers; the second
//        Charge attempt happens against a gateway that may or may
//        not have already debited the customer on the first call.
//
//   Without idempotency on the gateway side, both cases double-charge.
//   Adding application-level state (e.g., OrderStatusCharging) is the
//   alternative belt-and-suspenders approach (parked as A4 in the
//   roadmap) but does not eliminate the requirement on the gateway —
//   real payment providers (Stripe, Square, Adyen, PayPal, Braintree)
//   universally require an idempotency key per request and document
//   the contract above as their guarantee.
//
// Real adapters: pass orderID as the provider's `Idempotency-Key`
// header (Stripe), `idempotency_key` field (Square), or equivalent.
// Mock adapter: see internal/infrastructure/payment/mock_gateway.go
// for a sync.Map-backed implementation that exhibits the same contract.
type PaymentGateway interface {
	Charge(ctx context.Context, orderID uuid.UUID, amount float64) error
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
