package payment

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type Service struct {
	gateway   domain.PaymentGateway
	orderRepo domain.OrderRepository
	uow       application.UnitOfWork
	log       *mlog.Logger
}

// NewService takes the logger as an explicit dependency instead of
// reaching for zap's globals — that way fx can guarantee the logger is
// initialised before ProcessOrder starts handling Kafka messages.
//
// orderRepo is kept on the struct because the idempotency check
// (GetByID) and the success-path UpdateStatus run OUTSIDE uow.Do.
// The failure-path tx (UpdateStatus + outbox Create) reaches its
// repos through the Do closure parameter.
func NewService(
	gateway domain.PaymentGateway,
	orderRepo domain.OrderRepository,
	uow application.UnitOfWork,
	logger *mlog.Logger,
) domain.PaymentService {
	return &Service{
		gateway:   gateway,
		orderRepo: orderRepo,
		uow:       uow,
		log:       logger.With(mlog.String("component", "payment_service")),
	}
}

// ProcessOrder is at-least-once with respect to Kafka redelivery. Two
// race windows could otherwise cause double-charge:
//
//  a) gateway.Charge succeeds, then orderRepo.UpdateStatus(Confirmed)
//     fails → return err → Kafka redelivers → idempotency check sees
//     Status=Pending (UpdateStatus failed) → re-enters Charge path.
//  b) gateway.Charge fails, then the saga uow.Do fails → return err →
//     Kafka redelivers → re-enters Charge path against a gateway whose
//     first call may or may not have already debited the customer.
//
// Both are eliminated by the IDEMPOTENCY CONTRACT on PaymentGateway
// (see domain/payment.go): repeat Charge calls with the same orderID
// return the cached first-call result without re-charging. Real
// providers (Stripe, Square, Adyen, PayPal) implement this; the mock
// gateway implements it via sync.Map. ProcessOrder relies on that
// contract here — ANY adapter that violates it will produce duplicate
// charges under retry.
func (s *Service) ProcessOrder(ctx context.Context, event *domain.OrderCreatedEvent) error {
	s.log.Info(ctx, "Processing payment for order",
		tag.OrderID(event.OrderID), tag.Amount(event.Amount))

	// 1. Input Validation — return ErrInvalidPaymentEvent (NOT nil) so the
	// Kafka consumer can route the message to DLQ, emit a metric, and
	// commit the offset. Previously this function returned nil, which the
	// consumer interpreted as "success, commit offset" — permanently
	// losing the event without any operator-visible signal.
	if event.OrderID == uuid.Nil {
		s.log.Error(ctx, "Invalid OrderID (zero UUID)", tag.OrderID(event.OrderID))
		return fmt.Errorf("order_id is zero UUID: %w", domain.ErrInvalidPaymentEvent)
	}
	if event.Amount < 0 {
		s.log.Error(ctx, "Invalid Amount",
			tag.OrderID(event.OrderID), tag.Amount(event.Amount))
		return fmt.Errorf("amount=%v: %w", event.Amount, domain.ErrInvalidPaymentEvent)
	}

	// 2. Idempotency Check
	order, err := s.orderRepo.GetByID(ctx, event.OrderID)
	if err != nil {
		s.log.Error(ctx, "Failed to get order for idempotency check", tag.Error(err))
		return err
	}
	if order.Status() != domain.OrderStatusPending {
		s.log.Info(ctx, "Order already processed, skipping payment",
			tag.OrderID(event.OrderID), tag.Status(string(order.Status())))
		return nil
	}

	// 3. Call Payment Gateway
	if err := s.gateway.Charge(ctx, event.OrderID, event.Amount); err != nil {
		s.log.Error(ctx, "Payment failed, initiating Saga Rollback",
			tag.OrderID(event.OrderID), tag.Error(err))

		// Create Saga compensating event (order.failed) atomically with status update
		errUow := s.uow.Do(ctx, func(repos *application.Repositories) error {
			if updateErr := repos.Order.UpdateStatus(ctx, event.OrderID, domain.OrderStatusFailed); updateErr != nil {
				return updateErr
			}

			// Map directly off the inbound OrderCreatedEvent — payment
			// service consumes an event from Kafka, it doesn't have a
			// fresh domain.Order at hand. NewOrderFailedEvent lives
			// next to OrderCreatedEvent so the messaging contract is
			// in one place (see internal/domain/order_events.go).
			failedEvent := domain.NewOrderFailedEvent(*event, err.Error())
			payload, marshalErr := json.Marshal(failedEvent)
			if marshalErr != nil {
				return marshalErr
			}

			outboxEvent, outboxErr := domain.NewOrderFailedOutbox(payload)
			if outboxErr != nil {
				return fmt.Errorf("construct outbox event: %w", outboxErr)
			}
			_, createErr := repos.Outbox.Create(ctx, outboxEvent)
			return createErr
		})
		if errUow != nil {
			s.log.Error(ctx, "Failed to save saga compensating event",
				tag.OrderID(event.OrderID), tag.Error(errUow))
			return errUow // Return error to trigger Kafka retry
		}

		return nil // Consume event (don't retry indefinitely for business failures)
	}

	// 3. Update Order Status
	if err := s.orderRepo.UpdateStatus(ctx, event.OrderID, domain.OrderStatusConfirmed); err != nil {
		s.log.Error(ctx, "Failed to update order status to confirmed", tag.Error(err))
		return err // Retry update
	}

	s.log.Info(ctx, "Payment successful", tag.OrderID(event.OrderID))
	return nil
}
