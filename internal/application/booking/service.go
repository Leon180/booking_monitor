package booking

import (
	"context"
	"fmt"
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
	//   1. Mints orderID (UUIDv7) and computes reservedUntil = now +
	//      configured reservation window (D3 default 15 min; D8 will
	//      thread per-event overrides via events.reservation_window_seconds).
	//   2. Constructs the Order via `domain.NewReservation(...)` so
	//      every Pattern A invariant (incl. reservedUntil > now) is
	//      validated at the application boundary, BEFORE Redis is
	//      touched. Bad input rejects with a domain sentinel
	//      (ErrInvalidUserID / ErrInvalidQuantity / ErrInvalidReservedUntil)
	//      and never produces a Redis-side effect.
	//   3. Atomically deducts Redis inventory (the load-shed gate —
	//      sold-out attempts never touch DB) and publishes the order
	//      onto orders:stream. The order id, user id, event id,
	//      quantity, AND reservedUntil all ride along on the same
	//      atomic XADD so a Pattern A reservation is fully described
	//      by one stream message.
	//   4. Returns the constructed `domain.Order` (status =
	//      AwaitingPayment, reservedUntil set). The id flows handler
	//      response → stream → worker `domain.NewReservation(...)` →
	//      DB INSERT (orders.reserved_until column).
	//
	// Pattern A note: the worker no longer auto-charges. The returned
	// order is in `AwaitingPayment`, not `Pending` + Charging — the
	// client must call POST /api/v1/orders/:id/pay (D4) to actually
	// initiate payment. The legacy A4 payment_worker auto-skips
	// `AwaitingPayment` orders (it short-circuits on `status !=
	// Pending`), so leaving the outbox emit + payment_worker subscriber
	// in place during D3 is graceful — they go idle, no double-charge
	// risk. D7 cleans both up.
	//
	// On `domain.ErrSoldOut` the returned `domain.Order` is the zero
	// value — no order intent was recorded, no follow-up needed.
	// Callers branch on `errors.Is(err, domain.ErrSoldOut)`.
	BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (domain.Order, error)

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
	inventoryRepo     domain.InventoryRepository
	reservationWindow time.Duration
}

// NewService wires the booking Service. eventRepo / uow used to be
// parameters too, but PR 35 confirmed they were dead injection —
// BookTicket only touches Redis (the worker handles all DB work
// asynchronously) and GetBookingHistory only needs orderRepo.
//
// D3 adds the reservation window from BookingConfig — single global
// value for now; per-event override
// (events.reservation_window_seconds, schema added in 000012) lands
// in D8 when admin section CRUD is wired. Per-event read on the hot
// path would add a Postgres SELECT per booking, so the per-event
// value will be cached in Redis alongside `event:{id}:qty` rather
// than read from DB on every BookTicket.
func NewService(orderRepo domain.OrderRepository, inventoryRepo domain.InventoryRepository, cfg *config.Config) Service {
	return &service{
		orderRepo:         orderRepo,
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

func (s *service) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (domain.Order, error) {
	// 0. Mint the order id BEFORE Redis touches anything. The id flows
	//    end-to-end:
	//      handler response → orders:stream message → worker
	//      domain.NewReservation → DB orders.id → polling endpoint.
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

	// 1. Compute the reservation TTL. Use UTC to avoid timezone drift
	//    when the value gets serialized through Lua and re-parsed in
	//    the worker — every layer agrees on the same wire wallclock.
	reservedUntil := time.Now().Add(s.reservationWindow).UTC()

	// 2. Construct the domain.Order via the Pattern A factory.
	//    Invariant validation runs HERE at the application boundary,
	//    BEFORE Redis is touched. Bad input rejects with a sentinel
	//    (ErrInvalidUserID / ErrInvalidQuantity / ErrInvalidReservedUntil)
	//    and produces no Redis-side effect — the alternative ("deduct
	//    first, validate at the worker") would create a deduct →
	//    DLQ → revert cycle with no early signal to the caller.
	order, err := domain.NewReservation(orderID, userID, eventID, quantity, reservedUntil)
	if err != nil {
		return domain.Order{}, err
	}

	// 3. Atomic Deduct from Redis (hot path — Redis is the load-
	//    shedding gate; sold-out attempts never touch DB). reservedUntil
	//    rides along inside the same atomic XADD so the worker
	//    receives a fully-formed reservation in one stream message.
	success, err := s.inventoryRepo.DeductInventory(ctx, order.ID(), order.EventID(), order.UserID(), order.Quantity(), reservedUntil)
	if err != nil {
		return domain.Order{}, fmt.Errorf("redis inventory error: %w", err)
	}

	if !success {
		return domain.Order{}, domain.ErrSoldOut
	}

	// 4. Success — reservation intent is in the orders:stream message;
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
