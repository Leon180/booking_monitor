package domain

import (
	"context"

	"github.com/google/uuid"
)

//go:generate mockgen -source=payment.go -destination=../mocks/payment_mock.go -package=mocks

// PaymentGateway is the domain port for external payment processing.
// Stays in domain because it's a true domain integration boundary —
// no wire-format DTOs leak through (orderID is `uuid.UUID`, amount is
// `float64`, both pure value types). Implementations live in
// `internal/infrastructure/payment/` (MockGateway today; a real
// Stripe adapter would land alongside).
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
//   `application.PaymentService.ProcessOrder` runs at-least-once
//   (Kafka redelivery on failure). The ordering Charge → DB
//   UpdateStatus is a two-phase operation across distinct
//   infrastructures and CANNOT be wrapped in a tx. Two race windows
//   produce duplicate Charge calls on retry:
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

// `PaymentService` interface, `ErrInvalidPaymentEvent`,
// `OrderCreatedEvent`, and `OrderFailedEvent` moved to
// `internal/application/` in PR #38 (rule-7 audit). They are wire-
// format DTOs / application-service ports — application owns those
// concerns, not domain. PaymentGateway stays here because it IS a
// domain port: an external integration boundary with no DTO leaks.
