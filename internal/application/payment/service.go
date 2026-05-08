package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

type service struct {
	gateway   domain.PaymentIntentCreator
	orderRepo domain.OrderRepository
	log       *mlog.Logger
	now       func() time.Time // injectable for tests; production uses time.Now
}

// NewService wires the Pattern A /pay (D4) entry point. The pre-D7
// constructor accepted a `UnitOfWork` for the legacy A4 auto-charge
// path's saga emit; D7 deleted ProcessOrder so the dependency is
// gone. CreatePaymentIntent issues a single race-safe SQL UPDATE
// and has no transactional fanout of its own.
//
// `gateway` parameter narrowed from the combined `domain.PaymentGateway`
// to the read/write-split half `domain.PaymentIntentCreator` —
// CreatePaymentIntent only needs that half. fx providers in
// `cmd/booking-cli/server.go` advertise `PaymentIntentCreator`
// directly via `fx.As` so resolution by-type-key works.
//
// D4.1 NOTE: pre-D4.1 this constructor accepted `cfg *config.Config`
// for `BookingConfig.DefaultTicketPriceCents` + `DefaultCurrency` —
// every payment used the global default price. D4.1 reads price +
// currency directly from `order.AmountCents()` / `order.Currency()`
// (the snapshot frozen at book time by `domain.NewReservation`), so
// the config dependency is gone. The customer pays exactly what they
// were quoted, even if the merchant edits the ticket_type's price
// between book and pay (industry SOP — Stripe Checkout / Shopify /
// Eventbrite all freeze price at order create time).
func NewService(
	gateway domain.PaymentIntentCreator,
	orderRepo domain.OrderRepository,
	logger *mlog.Logger,
) Service {
	return &service{
		gateway:   gateway,
		orderRepo: orderRepo,
		log:       logger.With(mlog.String("component", "payment_service")),
		now:       time.Now,
	}
}

// CreatePaymentIntent is the Pattern A /pay (D4) entry point. See
// the Service interface doc for the high-level contract.
//
// Step-by-step:
//
//  1. Read the order. 404 if it doesn't exist.
//  2. Validate status == AwaitingPayment. Anything else (Paid /
//     Expired / PaymentFailed / Compensated / Pending / Charging /
//     Confirmed / Failed) → ErrOrderNotAwaitingPayment. The /pay
//     contract is "from a fresh reservation, you can pay once" —
//     re-payment of a Paid order is not a thing in our model.
//  3. Validate reserved_until > now. If the reservation has lapsed
//     (D6 sweeper hasn't run yet but the wall clock is past TTL),
//     return ErrReservationExpired. Charging here would soft-lock
//     inventory beyond what the user was promised.
//  4. Compute amount = quantity * priceCents (config). Currency from
//     config too. Per-event/section pricing lands in D8.
//  5. Call gateway.CreatePaymentIntent. Per the
//     PaymentIntentCreator idempotency contract, repeat calls with
//     the same orderID return the same PaymentIntent — making /pay
//     retry-safe at the gateway boundary without an application-
//     side cache.
//  6. Persist intent.ID via repo.SetPaymentIntentID. The race-safe
//     SQL predicate handles the webhook-flips-status race: if the
//     webhook (D5) flipped status to Paid milliseconds before our
//     write lands, the UPDATE 0-rows-affected → ErrOrderNotFound
//     surfaces back; the client can re-fetch the order to see the
//     actual state.
//  7. Return the PaymentIntent.
//
// Why no UoW transaction: the only write is SetPaymentIntentID
// (single statement, race-safe at SQL level). The gateway call is
// not in a tx (external IO), and there's no outbox emission. A UoW
// would be ceremony.
func (s *service) CreatePaymentIntent(ctx context.Context, orderID uuid.UUID) (domain.PaymentIntent, error) {
	if orderID == uuid.Nil {
		return domain.PaymentIntent{}, fmt.Errorf("CreatePaymentIntent: %w", domain.ErrInvalidOrderID)
	}

	order, err := s.orderRepo.GetByID(ctx, orderID)
	if err != nil {
		// ErrOrderNotFound passes through unwrapped so the handler's
		// errors.Is mapping catches it cleanly. Other errors get
		// wrapped for log clarity.
		if errors.Is(err, domain.ErrOrderNotFound) {
			return domain.PaymentIntent{}, err
		}
		s.log.Error(ctx, "CreatePaymentIntent: GetByID failed",
			tag.OrderID(orderID), tag.Error(err))
		return domain.PaymentIntent{}, fmt.Errorf("CreatePaymentIntent get order: %w", err)
	}

	if order.Status() != domain.OrderStatusAwaitingPayment {
		s.log.Info(ctx, "CreatePaymentIntent rejected: order not in AwaitingPayment",
			tag.OrderID(orderID), tag.Status(string(order.Status())))
		return domain.PaymentIntent{}, fmt.Errorf("CreatePaymentIntent status=%s: %w", order.Status(), ErrOrderNotAwaitingPayment)
	}

	if !order.ReservedUntil().After(s.now()) {
		s.log.Info(ctx, "CreatePaymentIntent rejected: reservation expired",
			tag.OrderID(orderID),
			mlog.String("reserved_until", order.ReservedUntil().Format(time.RFC3339)),
		)
		return domain.PaymentIntent{}, ErrReservationExpired
	}

	// D4.1 — read the snapshot frozen on the Order at book time
	// (`BookingService.BookTicket` computed `amount_cents = priceCents
	// × quantity` and stored it on the order). The customer pays what
	// they were quoted, even if the merchant edits the ticket_type's
	// price mid-checkout. See `docs/design/ticket_pricing.md` for the
	// schema-level rationale.
	//
	// `HasPriceSnapshot()` is a data-integrity guard, NOT a status
	// guard — it catches legacy rows / migration gaps where the
	// snapshot is missing. Wrapped as `ErrOrderMissingPriceSnapshot`
	// (NOT `ErrOrderNotAwaitingPayment`) so the handler logs at Error
	// (data bug pages on-call) instead of Warn (routine state
	// transition).
	if !order.HasPriceSnapshot() {
		s.log.Error(ctx, "CreatePaymentIntent rejected: order missing D4.1 price snapshot",
			tag.OrderID(orderID),
			mlog.Int64("amount_cents", order.AmountCents()),
			mlog.String("currency", order.Currency()),
		)
		return domain.PaymentIntent{}, fmt.Errorf("CreatePaymentIntent: order %s missing price snapshot (legacy row?): %w", orderID, ErrOrderMissingPriceSnapshot)
	}
	amount := order.AmountCents()
	currency := order.Currency()

	// metadata threads orderID through the gateway → webhook round-trip.
	// The D5 webhook handler reads `metadata.order_id` as the PRIMARY
	// lookup key; `payment_intent_id` is only the fallback. This rescues
	// the orphan-repair race documented at line 331 below: even if
	// `SetPaymentIntentID` fails after gateway success, the eventual
	// webhook still resolves back to the right order.
	//
	// Stripe convention: metadata is a small free-form map — keys
	// caller-defined, values stringified. A real adapter would pass
	// this verbatim to `paymentIntents.create({metadata: ...})`.
	intent, err := s.gateway.CreatePaymentIntent(ctx, orderID, amount, currency, map[string]string{
		"order_id": orderID.String(),
	})
	if err != nil {
		s.log.Error(ctx, "CreatePaymentIntent: gateway failed",
			tag.OrderID(orderID), tag.Error(err))
		return domain.PaymentIntent{}, fmt.Errorf("CreatePaymentIntent gateway: %w", err)
	}

	if err := s.orderRepo.SetPaymentIntentID(ctx, orderID, intent.ID); err != nil {
		// D5 widened SetPaymentIntentID's contract from "0 rows →
		// ErrOrderNotFound" to a 3-sentinel disambiguation. Each branch
		// here returns a distinct sentinel so the handler can map to a
		// precise HTTP code + public message:
		//   - ErrOrderNotFound        404 — row truly gone
		//   - ErrReservationExpired   409 — reserved_until elapsed during
		//                                   gateway round-trip; client
		//                                   should re-book (same race the
		//                                   SQL predicate has documented
		//                                   since D4)
		//   - ErrInvalidTransition    409 — status flipped (webhook /
		//                                   sweeper raced ahead) OR a
		//                                   different intent_id is
		//                                   already on the row (violates
		//                                   PaymentIntentCreator
		//                                   idempotency — should be
		//                                   impossible with a real
		//                                   gateway)
		switch {
		case errors.Is(err, domain.ErrOrderNotFound),
			errors.Is(err, domain.ErrReservationExpired),
			errors.Is(err, domain.ErrInvalidTransition):
			s.log.Info(ctx, "CreatePaymentIntent: order state changed between read and write",
				tag.OrderID(orderID),
				mlog.String("gateway_intent_id", intent.ID),
				tag.Error(err),
			)
			return domain.PaymentIntent{}, err
		}
		// Transient persist failure (DB outage, ctx cancellation,
		// etc.) AFTER the gateway has already registered the intent.
		// Logging gateway_intent_id here is operationally critical:
		// the next /pay retry will get the SAME intent (gateway-side
		// idempotency) but if retries don't happen, an operator can
		// recover the intent from logs without querying the gateway.
		s.log.Error(ctx, "CreatePaymentIntent: SetPaymentIntentID failed AFTER gateway succeeded",
			tag.OrderID(orderID),
			mlog.String("gateway_intent_id", intent.ID),
			tag.Error(err),
		)
		return domain.PaymentIntent{}, fmt.Errorf("CreatePaymentIntent persist: %w", err)
	}

	s.log.Info(ctx, "CreatePaymentIntent: intent created + persisted",
		tag.OrderID(orderID),
		mlog.String("payment_intent_id", intent.ID),
		mlog.Int64("amount_cents", amount),
		mlog.String("currency", currency),
	)
	return intent, nil
}
