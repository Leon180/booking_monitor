package payment

import (
	"context"
	"encoding/json"
	"errors"
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
) application.PaymentService {
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
func (s *Service) ProcessOrder(ctx context.Context, event *application.OrderCreatedEvent) error {
	s.log.Info(ctx, "Processing payment for order",
		tag.OrderID(event.OrderID), tag.Amount(event.Amount))

	// 1. Input Validation — return ErrInvalidPaymentEvent (NOT nil) so the
	// Kafka consumer can route the message to DLQ, emit a metric, and
	// commit the offset. Previously this function returned nil, which the
	// consumer interpreted as "success, commit offset" — permanently
	// losing the event without any operator-visible signal.
	if event.OrderID == uuid.Nil {
		s.log.Error(ctx, "Invalid OrderID (zero UUID)", tag.OrderID(event.OrderID))
		return fmt.Errorf("order_id is zero UUID: %w", application.ErrInvalidPaymentEvent)
	}
	if event.Amount < 0 {
		s.log.Error(ctx, "Invalid Amount",
			tag.OrderID(event.OrderID), tag.Amount(event.Amount))
		return fmt.Errorf("amount=%v: %w", event.Amount, application.ErrInvalidPaymentEvent)
	}

	// 2. Idempotency Check
	//
	// Skip if the order is already past Pending. After A4:
	//   - Confirmed/Failed/Compensated  → already resolved by us or the
	//                                     reconciler; nothing to do.
	//   - Charging                      → another worker grabbed this
	//                                     message between MarkCharging
	//                                     and Charge() (kafka rebalance,
	//                                     redelivery while in-flight).
	//                                     SKIP — let the reconciler
	//                                     resolve it via gateway.GetStatus().
	//                                     Calling Charge again here would
	//                                     race with the in-flight call.
	order, err := s.orderRepo.GetByID(ctx, event.OrderID)
	if err != nil {
		s.log.Error(ctx, "Failed to get order for idempotency check", tag.Error(err))
		return err
	}
	if order.Status() != domain.OrderStatusPending {
		s.log.Info(ctx, "Order not in Pending, skipping payment",
			tag.OrderID(event.OrderID), tag.Status(string(order.Status())))
		return nil
	}

	// 3. Mark Charging — A4 intent log entry. Written BEFORE calling
	// the gateway so a separate recon process can resolve a stuck
	// "Charging" order via gateway.GetStatus() if this worker dies
	// after MarkCharging but before MarkConfirmed/MarkFailed lands.
	//
	// ErrInvalidTransition here means another worker raced us through
	// the Pending check (rare but possible under concurrent Kafka
	// redelivery) — treat as idempotent: the other worker is now in
	// charge, we exit without calling Charge to avoid the double-charge
	// window. The Kafka offset gets committed (return nil) because
	// the other worker will produce a result.
	if err := s.orderRepo.MarkCharging(ctx, event.OrderID); err != nil {
		if errors.Is(err, domain.ErrInvalidTransition) {
			s.log.Info(ctx, "MarkCharging lost race to another worker, skipping",
				tag.OrderID(event.OrderID))
			return nil
		}
		s.log.Error(ctx, "MarkCharging failed", tag.Error(err))
		return err
	}

	// 4. Call Payment Gateway
	if err := s.gateway.Charge(ctx, event.OrderID, event.Amount); err != nil {
		s.log.Error(ctx, "Payment failed, initiating Saga Rollback",
			tag.OrderID(event.OrderID), tag.Error(err))

		// Create Saga compensating event (order.failed) atomically with status update
		errUow := s.uow.Do(ctx, func(repos *application.Repositories) error {
			if updateErr := repos.Order.MarkFailed(ctx, event.OrderID); updateErr != nil {
				return updateErr
			}

			// Map directly off the inbound OrderCreatedEvent — payment
			// service consumes an event from Kafka, it doesn't have a
			// fresh domain.Order at hand. NewOrderFailedEvent lives
			// next to OrderCreatedEvent so the messaging contract is
			// in one place (see internal/application/order_events.go).
			failedEvent := application.NewOrderFailedEvent(*event, err.Error())
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

	// 5. Update Order Status. MarkConfirmed enforces Charging→Confirmed
	// atomically in SQL (also accepts source=Pending during the A4
	// migration window).
	//
	// `ErrInvalidTransition` here is the recon-wins-race signal: the
	// reconciler picked up this order while we were calling the gateway,
	// got the same Charged verdict, and committed Confirmed first. Our
	// MarkConfirmed sees status=Confirmed (not Charging) and returns
	// ErrInvalidTransition. This is IDEMPOTENT SUCCESS — both paths
	// produced the same outcome, the database state is correct.
	//
	// Returning the error here would trigger Kafka redelivery → retry
	// storm → spurious error logs. Treat as success when GetByID
	// confirms the row is in a terminal state. Worker retry budget is
	// preserved for genuine failures.
	//
	// (Silent-failure-hunter B2: this is the fix.)
	if err := s.orderRepo.MarkConfirmed(ctx, event.OrderID); err != nil {
		if errors.Is(err, domain.ErrInvalidTransition) {
			// Re-read to confirm we lost the race (status is already
			// terminal) vs hit a real bug (status is in some weird
			// state). The latter shouldn't happen but if it does we
			// want to surface it loudly.
			cur, getErr := s.orderRepo.GetByID(ctx, event.OrderID)
			if getErr != nil {
				// Re-read itself failed (DB outage, ctx timeout). We
				// can't tell whether this is the recon-wins-race case
				// or a real bug — surface the GetByID failure so it
				// gets retried by Kafka. Surfacing this as the GetByID
				// error (not the original ErrInvalidTransition) makes
				// the actual root cause visible in logs.
				s.log.Error(ctx, "MarkConfirmed got ErrInvalidTransition; GetByID re-read also failed",
					tag.OrderID(event.OrderID),
					tag.Error(getErr),
					mlog.NamedError("original_error", err),
				)
				return getErr
			}
			if cur.Status() == domain.OrderStatusConfirmed ||
				cur.Status() == domain.OrderStatusFailed ||
				cur.Status() == domain.OrderStatusCompensated {
				s.log.Info(ctx, "MarkConfirmed lost race to reconciler — order already in terminal state",
					tag.OrderID(event.OrderID), tag.Status(string(cur.Status())))
				return nil
			}
			// ErrInvalidTransition + status NOT terminal = a real bug.
			// Fall through to the loud Error log + return below.
		}
		s.log.Error(ctx, "Failed to mark order confirmed", tag.Error(err))
		return err
	}

	s.log.Info(ctx, "Payment successful", tag.OrderID(event.OrderID))
	return nil
}
