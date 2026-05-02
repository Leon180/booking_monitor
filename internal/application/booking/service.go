package booking

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// tracerName scopes booking spans to their own subsystem in
// Jaeger / Tempo / Datadog APM. Subpackage-local since CP2.6.
const tracerName = "application/booking"

type Service interface {
	// BookTicket reserves inventory in Redis (the "gate" — load-shedding
	// happens here so failed-to-deduct attempts never touch DB). The
	// returned `domain.Order` is the canonical Order intent: its id
	// flows handler response → orders:stream message → worker
	// `domain.ReconstructOrder(...)` (the worker no longer re-validates;
	// invariants were enforced HERE via `domain.NewOrder`) → DB
	// persistence → outbox → Kafka order.created → payment + saga +
	// reconciler.
	//
	// CP2.6 alignment: `BookTicket` constructs a `domain.Order` via
	// `domain.NewOrder(...)` BEFORE the Redis deduct. Bad input is
	// rejected at the application boundary instead of slipping past
	// Redis and being caught by `NewOrder` in the worker (the prior
	// shape produced a Redis-deduct → worker DLQ → Redis-revert
	// sequence on every invariant violation, with no early signal to
	// the caller). Returning `domain.Order` instead of bare `uuid.UUID`
	// makes the signature symmetric with `GetOrder` and surfaces the
	// minted `CreatedAt` to callers without a follow-up GetByID.
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
	orderRepo     domain.OrderRepository
	inventoryRepo domain.InventoryRepository
}

// NewService wires the booking Service. eventRepo / uow used to be
// parameters too, but PR 35 confirmed they were dead injection —
// BookTicket only touches Redis (the worker handles all DB work
// asynchronously) and GetBookingHistory only needs orderRepo.
func NewService(orderRepo domain.OrderRepository, inventoryRepo domain.InventoryRepository) Service {
	return &service{
		orderRepo:     orderRepo,
		inventoryRepo: inventoryRepo,
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
	//      ReconstructOrder → DB orders.id → outbox → Kafka
	//      order.created → payment + saga + reconciler.
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

	// 1. Construct the domain.Order via the factory. CP2.6 alignment:
	//    invariant validation runs HERE at the application boundary,
	//    BEFORE Redis is touched. The prior shape deferred validation
	//    to `domain.NewOrder(...)` inside the worker — bad input would
	//    succeed at the API + Redis-deduct step, then fail at the
	//    worker, causing a Redis-side-effect-then-revert sequence with
	//    no early signal to the caller. Now: bad input rejects with
	//    `domain.ErrInvalidUserID` / `ErrInvalidQuantity` / etc. before
	//    any I/O.
	order, err := domain.NewOrder(orderID, userID, eventID, quantity)
	if err != nil {
		return domain.Order{}, err
	}

	// 2. Atomic Deduct from Redis (hot path — Redis is the load-
	//    shedding gate; sold-out attempts never touch DB).
	//
	// B3 sharding: DeductInventory returns the shard the deduct landed
	// in (0..N-1). With B3.1's INVENTORY_SHARDS=1 default, shard is
	// always 0. The shard is currently discarded — B3.2 will plumb it
	// through OrderCreatedEvent → saga compensator so RevertInventory
	// can revert to the same shard. For N=1 the discard is correct
	// because there's only one shard to revert to anyway.
	success, _, err := s.inventoryRepo.DeductInventory(ctx, order.ID(), order.EventID(), order.UserID(), order.Quantity())
	if err != nil {
		return domain.Order{}, fmt.Errorf("redis inventory error: %w", err)
	}

	if !success {
		return domain.Order{}, domain.ErrSoldOut
	}

	// 3. Success — order intent is in the orders:stream message; the
	//    worker will rehydrate via `domain.ReconstructOrder(...)` since
	//    invariants were validated above. Duplicate purchase is
	//    enforced by DB UNIQUE(user_id, event_id) at write time.
	return order, nil
}

func (s *service) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return s.orderRepo.GetByID(ctx, id)
}
