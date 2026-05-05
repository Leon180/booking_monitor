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
	//   1. Mints orderID (UUIDv7).
	//   2. Computes reservedUntil = now + configured reservation
	//      window (D3 default 15 min; D8 will thread per-event
	//      overrides).
	//   3. Atomically deducts Redis inventory (the load-shed gate —
	//      sold-out attempts never touch DB) and publishes the order
	//      onto orders:stream.
	//   4. Returns the constructed `domain.Order` (status =
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

// NewService wires the booking Service. ticketTypeRepo is retained
// for the runtime-metadata cold-fill repair path only.
//
// D3 adds the reservation window from BookingConfig — single global
// value for now; per-event override
// (events.reservation_window_seconds, schema added in 000012) lands
// in D8 when admin section CRUD is wired. Per-event read on the hot
// path would add a Postgres SELECT per booking, so the per-event
// value will eventually need the same runtime-cache treatment as the
// ticket_type metadata rather than a DB read on every BookTicket.
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

	// 1. Validate the caller-controlled fields BEFORE Redis is touched.
	if userID <= 0 {
		return domain.Order{}, domain.ErrInvalidUserID
	}
	if ticketTypeID == uuid.Nil {
		return domain.Order{}, domain.ErrInvalidOrderTicketTypeID
	}
	if quantity <= 0 {
		return domain.Order{}, domain.ErrInvalidQuantity
	}

	// 2. Compute the reservation TTL. Use UTC to avoid timezone drift
	//    when the value gets serialized through Lua and re-parsed in
	//    the worker — every layer agrees on the same wire wallclock.
	reservedUntil := time.Now().Add(s.reservationWindow).UTC()
	if reservedUntil.IsZero() || !reservedUntil.After(time.Now()) {
		return domain.Order{}, domain.ErrInvalidReservedUntil
	}

	// 3. Atomic deduct from Redis. On the success path Lua returns the
	//    event_id + price snapshot it loaded from ticket_type metadata.
	result, err := s.inventoryRepo.DeductInventory(
		ctx,
		orderID,
		ticketTypeID,
		userID,
		quantity,
		reservedUntil,
	)
	if err == nil {
		if !result.Accepted {
			return domain.Order{}, domain.ErrSoldOut
		}
		return domain.NewReservation(orderID, userID, result.EventID, ticketTypeID, quantity, reservedUntil, result.AmountCents, result.Currency)
	}

	if err == domain.ErrTicketTypeRuntimeMetadataMissing {
		if repairErr := s.repairRuntimeMetadata(ctx, ticketTypeID); repairErr != nil {
			return domain.Order{}, repairErr
		}
		result, err = s.inventoryRepo.DeductInventory(
			ctx,
			orderID,
			ticketTypeID,
			userID,
			quantity,
			reservedUntil,
		)
		if err == nil {
			if !result.Accepted {
				return domain.Order{}, domain.ErrSoldOut
			}
			return domain.NewReservation(orderID, userID, result.EventID, ticketTypeID, quantity, reservedUntil, result.AmountCents, result.Currency)
		}
		if err == domain.ErrTicketTypeRuntimeMetadataMissing {
			return domain.Order{}, fmt.Errorf("redis runtime metadata still missing after repair ticket_type_id=%s: %w", ticketTypeID, err)
		}
	}

	if err != nil {
		return domain.Order{}, fmt.Errorf("redis inventory error: %w", err)
	}
	return domain.Order{}, domain.ErrSoldOut
}

func (s *service) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return s.orderRepo.GetByID(ctx, id)
}

func (s *service) repairRuntimeMetadata(ctx context.Context, ticketTypeID uuid.UUID) error {
	tt, err := s.ticketTypeRepo.GetByID(ctx, ticketTypeID)
	if err != nil {
		return fmt.Errorf("BookTicket lookup ticket_type_id=%s: %w", ticketTypeID, err)
	}
	if err := s.inventoryRepo.SetTicketTypeMetadata(ctx, tt); err != nil {
		return fmt.Errorf("BookTicket repair metadata ticket_type_id=%s: %w", ticketTypeID, err)
	}
	return nil
}
