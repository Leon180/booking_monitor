package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// revertCompensationTimeout bounds the detached revert call when
// Stage 5 needs to roll back a Redis Lua deduct after Kafka publish
// failed. 3s is generous given revert.lua is a single EXISTS+INCRBY
// pipeline on a healthy Redis; if Redis is so degraded that revert
// takes >3s, the drift reconciler will catch the leaked inventory
// on its next sweep.
const revertCompensationTimeout = 3 * time.Second

// ErrStage5PublishFailed is the OUTER wrapper for any
// publish-or-revert failure on the Stage 5 hot path. Exists for two
// reasons:
//
//  1. HTTP layer ordering. MapBookingError checks `errors.Is(err, X)`
//     in a switch; the first match wins. Today no path can plausibly
//     produce a publishErr that transitively wraps a `domain.ErrSoldOut`
//     (sold-out is checked + returned BEFORE publish), but a future
//     refactor could introduce one — at which point a Kafka outage
//     would silently return 409 "sold out" to the client instead of
//     a paged 503. Wrapping with this sentinel at the service boundary
//     gives MapBookingError a stable hook to map this case to 503
//     explicitly, regardless of what publishErr's chain contains.
//  2. Operator log queries. A single sentinel name in the structured
//     log makes "find all Stage 5 publish failures across the fleet"
//     a `errors.Is == ErrStage5PublishFailed` query rather than a
//     pattern-match against multiple wrap-string variants.
//
// Defense-in-depth, not a current-bug fix.
var ErrStage5PublishFailed = errors.New("stage5 kafka intake publish failed")

// kafkaIntakeService is the Stage 5 BookingService variant.
//
// It differs from `service` (Stages 2-4) in exactly one place: after
// the Lua atomic deduct succeeds, it publishes a Kafka message with
// acks=all instead of relying on the Lua's XADD to orders:stream.
// On Kafka publish failure it calls RevertInventory to re-add the
// inventory before returning the error.
//
// This implements the Damai-aligned production pattern where the
// durable queue layer is replicated Kafka, not ephemeral Redis Stream.
// See `docs/d12/README.md` Stage 5 row for the full architecture matrix.
type kafkaIntakeService struct {
	orderRepo         domain.OrderRepository
	ticketTypeRepo    domain.TicketTypeRepository
	inventoryRepo     domain.Stage5InventoryRepository
	publisher         IntakePublisher
	metrics           Stage5Metrics
	reservationWindow time.Duration
	logger            *mlog.Logger
}

// NewKafkaIntakeService wires the Stage 5 booking Service. It
// satisfies the same Service interface as the Stages 2-4 impl so the
// HTTP handler doesn't change.
//
// inventoryRepo is the EXTENDED Stage5InventoryRepository which
// exposes DeductInventoryNoStream alongside the standard methods.
// metrics is the Stage5Metrics port (prometheus-backed impl lives in
// observability; tests can pass Stage5NopMetrics{}).
func NewKafkaIntakeService(
	orderRepo domain.OrderRepository,
	ticketTypeRepo domain.TicketTypeRepository,
	inventoryRepo domain.Stage5InventoryRepository,
	publisher IntakePublisher,
	metrics Stage5Metrics,
	cfg *config.Config,
	logger *mlog.Logger,
) Service {
	return &kafkaIntakeService{
		orderRepo:         orderRepo,
		ticketTypeRepo:    ticketTypeRepo,
		inventoryRepo:     inventoryRepo,
		publisher:         publisher,
		metrics:           metrics,
		reservationWindow: cfg.Booking.ReservationWindow,
		logger:            logger.With(mlog.String("component", "stage5_booking_service")),
	}
}

func (s *kafkaIntakeService) GetBookingHistory(ctx context.Context, page, pageSize int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize
	return s.orderRepo.ListOrders(ctx, pageSize, offset, status)
}

func (s *kafkaIntakeService) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return s.orderRepo.GetByID(ctx, id)
}

func (s *kafkaIntakeService) BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, quantity int) (domain.Order, error) {
	// Steps 0-2 are identical to Stages 2-4: mint id, validate, compute TTL.
	orderID, err := uuid.NewV7()
	if err != nil {
		return domain.Order{}, fmt.Errorf("generate order id: %w", err)
	}
	if userID <= 0 {
		return domain.Order{}, domain.ErrInvalidUserID
	}
	if ticketTypeID == uuid.Nil {
		return domain.Order{}, domain.ErrInvalidOrderTicketTypeID
	}
	if quantity <= 0 {
		return domain.Order{}, domain.ErrInvalidQuantity
	}
	reservedUntil := time.Now().Add(s.reservationWindow).UTC()
	if reservedUntil.IsZero() || !reservedUntil.After(time.Now()) {
		return domain.Order{}, domain.ErrInvalidReservedUntil
	}

	// Step 3a: atomic Lua deduct WITHOUT XADD. Returns metadata for the
	// Kafka message construction below.
	result, err := s.deductWithMetadataRepair(ctx, ticketTypeID, quantity)
	if err != nil {
		return domain.Order{}, err
	}
	if !result.Accepted {
		return domain.Order{}, domain.ErrSoldOut
	}

	// Step 3b: publish booking message to Kafka with acks=all. This is
	// the durability boundary — Stages 2-4 rely on Redis Stream which
	// loses messages on Redis crash; Stage 5 ensures the booking is
	// safely replicated before returning 202.
	msg := IntakeMessage{
		OrderID:       orderID,
		UserID:        userID,
		EventID:       result.EventID,
		TicketTypeID:  ticketTypeID,
		Quantity:      quantity,
		ReservedUntil: reservedUntil.Unix(),
		AmountCents:   result.AmountCents,
		Currency:      result.Currency,
	}
	if publishErr := s.publisher.PublishIntake(ctx, msg); publishErr != nil {
		// Step 3c (compensation): the Lua deduct succeeded but Kafka
		// publish failed. Revert the Redis qty so other bookings can
		// claim the ticket.
		//
		// CRITICAL: use a DETACHED context for the revert call, not
		// the request `ctx`. If the request was cancelled mid-publish
		// (client disconnect / upstream timeout), `ctx` is already
		// dead — reusing it would immediately fail the revert with
		// `context.Canceled` and leak the inventory unit permanently.
		// Mirrors the pattern in `handleCreateEvent`'s
		// compensateDanglingEvent.
		//
		// compensationID := orderID-as-string keeps revert.lua's
		// idempotency-key shape consistent with the saga compensator.
		compensationID := orderID.String()
		revertCtx, cancel := context.WithTimeout(context.Background(), revertCompensationTimeout)
		defer cancel()
		if revertErr := s.inventoryRepo.RevertInventory(revertCtx, ticketTypeID, quantity, compensationID); revertErr != nil {
			// Drift: Lua deducted, Kafka publish failed, revert also
			// failed. This is the dual-write race scenario the drift
			// reconciler detects. Record reverted=false so Prometheus
			// alert can fire on rate.
			s.metrics.RecordPublishFailure(false)
			s.logger.Error(ctx, "stage5 drift: Kafka publish failed AND inventory revert failed",
				tag.Error(publishErr),
				tag.OrderID(orderID),
				tag.TicketTypeID(ticketTypeID),
				mlog.NamedError("revert_error", revertErr),
			)
			// errors.Join preserves both errors in the chain so
			// callers using errors.Is on either sentinel still match.
			// `%v` on revertErr (the previous impl) silently broke
			// `errors.Is(err, revertSentinel)`.
			//
			// Outer wrap with ErrStage5PublishFailed gives the HTTP
			// error mapper (stagehttp.MapBookingError) a stable
			// match-first sentinel to map this branch to 503 — see
			// the sentinel's docstring for the defense-in-depth
			// rationale.
			return domain.Order{}, fmt.Errorf("stage5 drift (publish+revert both failed): %w: %w",
				ErrStage5PublishFailed, errors.Join(publishErr, revertErr))
		}
		// Publish failed but revert succeeded — clean compensation.
		// log.Error (not Warn) because a publish-fail is an SLA-impacting
		// event for ops: client gets a 5xx, durability gate breached.
		s.metrics.RecordPublishFailure(true)
		s.logger.Error(ctx, "stage5 Kafka publish failed; inventory reverted",
			tag.Error(publishErr),
			tag.OrderID(orderID),
			tag.TicketTypeID(ticketTypeID),
		)
		return domain.Order{}, fmt.Errorf("%w (reverted): %w", ErrStage5PublishFailed, publishErr)
	}

	// Step 4: build the in-memory domain.Order to return. The actual
	// PG row will be written by the Stage 5 worker after consuming the
	// Kafka message (mirrors Stage 3/4 pattern — async PG INSERT).
	return domain.NewReservation(
		orderID,
		userID,
		result.EventID,
		ticketTypeID,
		quantity,
		reservedUntil,
		result.AmountCents,
		result.Currency,
	)
}

// deductWithMetadataRepair is the Stage 5 equivalent of the
// Stages 2-4 service's inline retry-after-metadata-repair logic.
// Stage 5 uses DeductInventoryNoStream instead of DeductInventory,
// otherwise the cold-fill repair semantics are unchanged.
func (s *kafkaIntakeService) deductWithMetadataRepair(
	ctx context.Context,
	ticketTypeID uuid.UUID,
	quantity int,
) (domain.DeductInventoryResult, error) {
	result, err := s.inventoryRepo.DeductInventoryNoStream(ctx, ticketTypeID, quantity)
	if err == nil {
		return result, nil
	}
	if !errors.Is(err, domain.ErrTicketTypeRuntimeMetadataMissing) {
		return domain.DeductInventoryResult{}, fmt.Errorf("redis inventory error: %w", err)
	}
	// Cold-fill repair: load ticket_type from PG and reseed Redis
	// metadata key, then retry the Lua call.
	tt, repairErr := s.ticketTypeRepo.GetByID(ctx, ticketTypeID)
	if repairErr != nil {
		return domain.DeductInventoryResult{}, fmt.Errorf("BookTicket lookup ticket_type_id=%s: %w", ticketTypeID, repairErr)
	}
	if metaErr := s.inventoryRepo.SetTicketTypeMetadata(ctx, tt); metaErr != nil {
		return domain.DeductInventoryResult{}, fmt.Errorf("BookTicket repair metadata ticket_type_id=%s: %w", ticketTypeID, metaErr)
	}
	result, err = s.inventoryRepo.DeductInventoryNoStream(ctx, ticketTypeID, quantity)
	if err != nil {
		if errors.Is(err, domain.ErrTicketTypeRuntimeMetadataMissing) {
			// Rare race: TTL on the meta key expired between
			// SetTicketTypeMetadata and the second deduct call, or
			// the qty key vanished too. The HTTP handler only sees
			// a 500 counter, so log here at Error level with full
			// ticket_type context so ops can correlate to the Redis
			// TTL config without request tracing.
			s.logger.Error(ctx, "stage5 redis metadata still missing after repair",
				tag.TicketTypeID(ticketTypeID),
				tag.Error(err),
			)
			return domain.DeductInventoryResult{}, fmt.Errorf("redis runtime metadata still missing after repair ticket_type_id=%s: %w", ticketTypeID, err)
		}
		return domain.DeductInventoryResult{}, fmt.Errorf("redis inventory error after repair: %w", err)
	}
	return result, nil
}
