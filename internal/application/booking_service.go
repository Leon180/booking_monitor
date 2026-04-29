package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

const tracerName = "application/service"

type BookingService interface {
	// BookTicket reserves inventory in Redis (the "gate" — load-shedding
	// happens here so failed-to-deduct attempts never touch DB). On
	// success the returned uuid.UUID is the canonical order id: the
	// handler echoes it to the client, the Lua script writes it into
	// the orders:stream message, and the worker re-uses it via
	// `domain.NewOrder(id, ...)` so DB persistence + saga + recon all
	// share a single id across PEL retries.
	//
	// On `domain.ErrSoldOut` the second return value is `uuid.Nil` —
	// no order intent was recorded, no follow-up is needed.
	BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (uuid.UUID, error)

	GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error)

	// GetOrder returns the persisted Order by id. Returns
	// `domain.ErrOrderNotFound` when no row matches — including the
	// brief sub-second window after BookTicket returns but before the
	// worker has persisted the row, which is expected and the client
	// should retry with backoff.
	GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error)
}

type bookingService struct {
	orderRepo     domain.OrderRepository
	inventoryRepo domain.InventoryRepository
}

// NewBookingService wires the BookingService. eventRepo / uow used to
// be parameters too, but PR 35 confirmed they were dead injection —
// BookTicket only touches Redis (the worker handles all DB work
// asynchronously) and GetBookingHistory only needs orderRepo.
func NewBookingService(orderRepo domain.OrderRepository, inventoryRepo domain.InventoryRepository) BookingService {
	return &bookingService{
		orderRepo:     orderRepo,
		inventoryRepo: inventoryRepo,
	}
}

func (s *bookingService) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize

	return s.orderRepo.ListOrders(ctx, pageSize, offset, status)
}

func (s *bookingService) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, quantity int) (uuid.UUID, error) {
	// 0. Mint the order id BEFORE Redis touches anything. The id flows
	//    end-to-end:
	//      handler response → orders:stream message → worker NewOrder
	//      → DB orders.id → outbox → Kafka order.created → payment +
	//      saga + reconciler.
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
		return uuid.Nil, fmt.Errorf("generate order id: %w", err)
	}

	// 1. Atomic Deduct from Redis (hot path — Redis is the load-
	//    shedding gate; sold-out attempts never touch DB).
	success, err := s.inventoryRepo.DeductInventory(ctx, orderID, eventID, userID, quantity)
	if err != nil {
		return uuid.Nil, fmt.Errorf("redis inventory error: %w", err)
	}

	if !success {
		return uuid.Nil, domain.ErrSoldOut
	}

	// 2. Success — order_id is in the orders:stream message; the worker
	//    will persist via `domain.NewOrder(orderID, ...)`. Duplicate
	//    purchase is enforced by DB UNIQUE(user_id, event_id).
	return orderID, nil
}

func (s *bookingService) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return s.orderRepo.GetByID(ctx, id)
}
