package booking

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
)

// tracerName scopes booking spans to their own subsystem in
// Jaeger / Tempo / Datadog APM. Subpackage-local since CP2.6.
const tracerName = "application/booking"

type Service interface {
	// BookTicket creates a Pattern A reservation:
	//
	//   1. Mints orderID (UUIDv7).
	//   2. Looks up the chosen TicketType to derive its eventID,
	//      priceCents, and currency. Price is snapshotted on the
	//      Order at book time (industry SOP — Stripe Checkout,
	//      Shopify, Eventbrite all freeze the price at the order/cart
	//      so the customer pays what they were quoted).
	//   3. Computes reservedUntil = now + configured reservation
	//      window (D3 default 15 min; D8 will thread per-event
	//      overrides).
	//   4. Constructs the Order via `domain.NewReservation(...)` so
	//      every Pattern A + D4.1 invariant (reservedUntil > now,
	//      amount_cents > 0, valid currency) is validated at the
	//      application boundary BEFORE Redis is touched. Bad input
	//      rejects with a domain sentinel and never produces a
	//      Redis-side effect.
	//   5. Atomically deducts Redis inventory (the load-shed gate —
	//      sold-out attempts never touch DB) and publishes the order
	//      onto orders:stream.
	//   6. Returns the constructed `domain.Order` (status =
	//      AwaitingPayment, reservedUntil + amountCents + currency
	//      set). The id flows handler response → stream → worker →
	//      DB INSERT.
	//
	// D4.1 takes ticketTypeID as the customer-facing argument (KKTIX
	// 票種 model — the customer chooses a ticket_type, and the
	// ticket_type knows its event). Pre-D4.1 BookTicket took eventID
	// directly; the API DTO has been updated accordingly.
	//
	// Pattern A note: the worker no longer auto-charges. The returned
	// order is in `AwaitingPayment`. The client must call
	// POST /api/v1/orders/:id/pay (D4) to actually initiate payment.
	//
	// Errors:
	//   - domain.ErrTicketTypeNotFound  ticket_type_id doesn't exist
	//   - domain.ErrSoldOut             Redis inventory exhausted
	//   - domain.ErrInvalid*            invariant violation surfaced
	//                                   via NewReservation
	BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, quantity int) (domain.Order, error)

	GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error)

	// GetOrder returns the persisted Order by id. Returns
	// `domain.ErrOrderNotFound` when no row matches — including the
	// brief sub-second window after BookTicket returns but before the
	// worker has persisted the row, which is expected and the client
	// should retry with backoff.
	GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error)
}

type service struct {
	orderRepo         domain.OrderRepository
	ticketTypeRepo    domain.TicketTypeRepository
	inventoryRepo     domain.InventoryRepository
	reservationWindow time.Duration
}

// NewService wires the booking Service. D4.1 adds ticketTypeRepo so
// BookTicket can look up the chosen ticket_type's price + event_id
// before constructing the reservation.
//
// D3 adds the reservation window from BookingConfig — single global
// value for now; per-event override
// (events.reservation_window_seconds, schema added in 000012) lands
// in D8 when admin section CRUD is wired. Per-event read on the hot
// path would add a Postgres SELECT per booking, so the per-event
// value will be cached in Redis alongside `event:{id}:qty` rather
// than read from DB on every BookTicket.
func NewService(orderRepo domain.OrderRepository, ticketTypeRepo domain.TicketTypeRepository, inventoryRepo domain.InventoryRepository, cfg *config.Config) Service {
	return &service{
		orderRepo:         orderRepo,
		ticketTypeRepo:    ticketTypeRepo,
		inventoryRepo:     inventoryRepo,
		reservationWindow: cfg.Booking.ReservationWindow,
	}
}

func (s *service) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize

	return s.orderRepo.ListOrders(ctx, pageSize, offset, status)
}

func (s *service) BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, quantity int) (domain.Order, error) {
	// 0. Mint the order id BEFORE Redis touches anything. The id flows
	//    end-to-end:
	//      handler response → orders:stream message → worker → DB →
	//      polling endpoint.
	//    Generating in the worker (the pre-PR-47 behaviour) made every
	//    PEL retry produce a fresh uuid, so retries diverged from the
	//    id the client already had in hand. With the id minted here
	//    once, retries are deterministic at the id level — DB UNIQUE
	//    catches duplicates, but more importantly the client's id
	//    stays valid for the polling endpoint.
	orderID, err := uuid.NewV7()
	if err != nil {
		// crypto/rand failure is the only path here. Vanishingly rare
		// (entropy exhaustion / fuzz) but surface so the API returns
		// 500 instead of producing a zero-UUID order.
		return domain.Order{}, fmt.Errorf("generate order id: %w", err)
	}

	// 1. Look up the ticket_type to derive event_id + price snapshot.
	//    D4.1 introduces this — pre-D4.1 the API took event_id directly
	//    and price came from a global BookingConfig. Now the customer
	//    chooses a ticket_type (KKTIX vocabulary) and the ticket_type
	//    drives both inventory selection and pricing.
	//
	//    ErrTicketTypeNotFound is wrapped (not bare-returned) so the
	//    log entry shows it came from BookTicket, not e.g. the recon
	//    sweeper which also calls GetByID. The HTTP handler still
	//    `errors.Is`-checks against the sentinel for the 404 mapping —
	//    %w preserves the chain.
	tt, err := s.ticketTypeRepo.GetByID(ctx, ticketTypeID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("BookTicket lookup ticket_type_id=%s: %w", ticketTypeID, err)
	}
	eventID := tt.EventID()
	// 2. Snapshot the price: amount_cents = priceCents * quantity, in
	//    the ticket_type's own currency. Computed up front so the
	//    invariant check in NewReservation runs against the snapshot.
	//
	//    Overflow guard: int64 multiplication wraps silently to a
	//    negative value at MaxInt64; without this guard a malformed
	//    ticket_type (priceCents near MaxInt64) + a normal quantity
	//    would surface as ErrInvalidAmountCents in NewReservation,
	//    misdirecting diagnosis. The guard is paranoia (no real ticket
	//    costs MaxInt64/quantity cents) but the cost is one branch.
	priceCents := tt.PriceCents()
	if quantity > 0 && priceCents > math.MaxInt64/int64(quantity) {
		return domain.Order{}, fmt.Errorf("BookTicket: amount overflow ticket_type_id=%s price_cents=%d quantity=%d", ticketTypeID, priceCents, quantity)
	}
	amountCents := priceCents * int64(quantity)
	currency := tt.Currency()

	// 3. Compute the reservation TTL. Use UTC to avoid timezone drift
	//    when the value gets serialized through Lua and re-parsed in
	//    the worker — every layer agrees on the same wire wallclock.
	reservedUntil := time.Now().Add(s.reservationWindow).UTC()

	// 4. Construct the domain.Order via the Pattern A factory.
	//    Invariant validation runs HERE at the application boundary,
	//    BEFORE Redis is touched. Bad input rejects with a sentinel
	//    (ErrInvalidUserID / ErrInvalidQuantity / ErrInvalidReservedUntil
	//    / ErrInvalidAmountCents / ErrInvalidCurrency) and produces no
	//    Redis-side effect — the alternative ("deduct first, validate
	//    at the worker") would create a deduct → DLQ → revert cycle
	//    with no early signal to the caller.
	order, err := domain.NewReservation(orderID, userID, eventID, ticketTypeID, quantity, reservedUntil, amountCents, currency)
	if err != nil {
		return domain.Order{}, err
	}

	// 5. Atomic Deduct from Redis (hot path — Redis is the load-
	//    shedding gate; sold-out attempts never touch DB). reservedUntil
	//    + ticketTypeID + amountCents + currency all ride along inside
	//    the same atomic XADD so the worker receives a fully-formed
	//    reservation in one stream message — no second Redis trip / DB
	//    lookup at the worker boundary, and no race against admin price
	//    edits between book and worker-process time.
	success, err := s.inventoryRepo.DeductInventory(
		ctx,
		order.ID(),
		order.EventID(),
		order.TicketTypeID(),
		order.UserID(),
		order.Quantity(),
		reservedUntil,
		order.AmountCents(),
		order.Currency(),
	)
	if err != nil {
		return domain.Order{}, fmt.Errorf("redis inventory error: %w", err)
	}

	if !success {
		return domain.Order{}, domain.ErrSoldOut
	}

	// 6. Success — reservation intent is in the orders:stream message;
	//    the worker will validate-and-INSERT it as
	//    `status=awaiting_payment, reserved_until=...` then continue
	//    to the outbox. Duplicate purchase is still enforced by DB
	//    UNIQUE(user_id, event_id) at write time. The client must call
	//    POST /api/v1/orders/:id/pay (D4) to actually charge — the
	//    legacy auto-charge path is gone for Pattern A.
	return order, nil
}

func (s *service) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return s.orderRepo.GetByID(ctx, id)
}
