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
	reservationWindow time.Duration
	logger            *mlog.Logger
}

// NewKafkaIntakeService wires the Stage 5 booking Service. It
// satisfies the same Service interface as the Stages 2-4 impl so the
// HTTP handler doesn't change.
//
// inventoryRepo is the EXTENDED Stage5InventoryRepository which
// exposes DeductInventoryNoStream alongside the standard methods.
func NewKafkaIntakeService(
	orderRepo domain.OrderRepository,
	ticketTypeRepo domain.TicketTypeRepository,
	inventoryRepo domain.Stage5InventoryRepository,
	publisher IntakePublisher,
	cfg *config.Config,
	logger *mlog.Logger,
) Service {
	return &kafkaIntakeService{
		orderRepo:         orderRepo,
		ticketTypeRepo:    ticketTypeRepo,
		inventoryRepo:     inventoryRepo,
		publisher:         publisher,
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
		// claim the ticket. compensationID := orderID-as-string keeps
		// revert.lua's idempotency-key shape consistent with the saga
		// compensator path.
		compensationID := orderID.String()
		if revertErr := s.inventoryRepo.RevertInventory(ctx, ticketTypeID, quantity, compensationID); revertErr != nil {
			// Drift: Lua deducted, Kafka publish failed, revert also failed.
			// This is the dual-write race scenario the drift reconciler
			// detects. Surface both errors and let the operator decide.
			s.logger.Error(ctx, "stage5 drift: Kafka publish failed AND inventory revert failed",
				tag.Error(publishErr),
				tag.OrderID(orderID),
				tag.TicketTypeID(ticketTypeID),
				mlog.NamedError("revert_error", revertErr),
			)
			return domain.Order{}, fmt.Errorf("stage5 drift (publish+revert both failed): publish=%w; revert=%v", publishErr, revertErr)
		}
		s.logger.Warn(ctx, "stage5 Kafka publish failed; inventory reverted",
			tag.Error(publishErr),
			tag.OrderID(orderID),
			tag.TicketTypeID(ticketTypeID),
		)
		return domain.Order{}, fmt.Errorf("stage5 intake publish failed (reverted): %w", publishErr)
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
			return domain.DeductInventoryResult{}, fmt.Errorf("redis runtime metadata still missing after repair ticket_type_id=%s: %w", ticketTypeID, err)
		}
		return domain.DeductInventoryResult{}, fmt.Errorf("redis inventory error after repair: %w", err)
	}
	return result, nil
}
