package synclua

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/mocks"
)

// service_test.go covers the Redis-side branches and Go-side
// validation paths of synclua.Service. The DB-INSERT branches
// (happy path, 23505 → ErrUserAlreadyBought, generic INSERT
// failures triggering revert) require a real Postgres and live in
// `test/integration/postgres/synclua_booking_test.go` — keeping
// those out of this file mirrors Stage 1's split (no
// service_test.go alongside `internal/application/booking/sync/`;
// integration suite owns those cases).
//
// The Deducter is faked here rather than miniredis-backed: Redis
// research from the plan flagged miniredis Lua-fidelity gaps as a
// risk, so we keep miniredis out of unit tests entirely and let
// testcontainers Redis pin Lua semantics in the integration slice.

// fakeDeducter scripts a multi-call Deduct sequence so the test can
// exercise the metadata_missing → repair → retry path without
// running real Lua. Each successive Deduct call consumes one entry
// from `responses`; calls beyond the scripted length surface a
// distinctive error so a buggy retry loop doesn't silently fall off
// the end.
type fakeDeducter struct {
	responses []deductResponse
	calls     int
}

type deductResponse struct {
	result domain.DeductInventoryResult
	err    error
}

func (f *fakeDeducter) Deduct(_ context.Context, _ uuid.UUID, _ int) (domain.DeductInventoryResult, error) {
	if f.calls >= len(f.responses) {
		return domain.DeductInventoryResult{}, errors.New("fakeDeducter: unexpected extra call")
	}
	resp := f.responses[f.calls]
	f.calls++
	return resp.result, resp.err
}

// newServiceForTest assembles a Service with a 15-minute reservation
// window, gomock-controlled repos, and the supplied Deducter. db is
// nil — slice-2 tests never reach insertOrder.
func newServiceForTest(t *testing.T, ctrl *gomock.Controller, deducter Deducter) (
	*Service,
	*mocks.MockOrderRepository,
	*mocks.MockTicketTypeRepository,
	*mocks.MockInventoryRepository,
) {
	t.Helper()
	orderRepo := mocks.NewMockOrderRepository(ctrl)
	ttRepo := mocks.NewMockTicketTypeRepository(ctrl)
	invRepo := mocks.NewMockInventoryRepository(ctrl)
	cfg := &config.Config{Booking: config.BookingConfig{ReservationWindow: 15 * time.Minute}}
	svc := NewService(nil, deducter, orderRepo, ttRepo, invRepo, cfg)
	return svc, orderRepo, ttRepo, invRepo
}

func TestService_BookTicket_ValidationErrors(t *testing.T) {
	t.Parallel()

	validTicketType := uuid.New()

	cases := []struct {
		name         string
		userID       int
		ticketTypeID uuid.UUID
		quantity     int
		want         error
	}{
		{"negative_user_id", -1, validTicketType, 1, domain.ErrInvalidUserID},
		{"zero_user_id", 0, validTicketType, 1, domain.ErrInvalidUserID},
		{"nil_ticket_type", 1, uuid.Nil, 1, domain.ErrInvalidOrderTicketTypeID},
		{"zero_quantity", 1, validTicketType, 0, domain.ErrInvalidQuantity},
		{"negative_quantity", 1, validTicketType, -3, domain.ErrInvalidQuantity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			// fakeDeducter with empty script — any Deduct call would
			// fail the test, proving validation short-circuits before
			// Redis is touched.
			svc, _, _, _ := newServiceForTest(t, ctrl, &fakeDeducter{})

			_, err := svc.BookTicket(context.Background(), tc.userID, tc.ticketTypeID, tc.quantity)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestService_BookTicket_SoldOut(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	deducter := &fakeDeducter{
		responses: []deductResponse{
			{result: domain.DeductInventoryResult{Accepted: false}, err: nil},
		},
	}
	svc, _, _, _ := newServiceForTest(t, ctrl, deducter)

	_, err := svc.BookTicket(context.Background(), 1, uuid.New(), 1)
	assert.ErrorIs(t, err, domain.ErrSoldOut)
	assert.Equal(t, 1, deducter.calls)
}

func TestService_BookTicket_GenericDeducterError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	redisDown := errors.New("redis: connection refused")
	deducter := &fakeDeducter{
		responses: []deductResponse{{err: redisDown}},
	}
	svc, _, _, _ := newServiceForTest(t, ctrl, deducter)

	_, err := svc.BookTicket(context.Background(), 1, uuid.New(), 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, redisDown, "deduct error should wrap, not replace, the underlying redis error")
}

func TestService_BookTicket_MetadataMissing_RepairsAndRetries(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	ticketTypeID := uuid.New()
	eventID := uuid.New()
	rebuilt, err := domain.NewTicketType(ticketTypeID, eventID, "GA", 5000, "usd", 100, nil, nil, nil, "")
	require.NoError(t, err)

	// First Deduct: metadata_missing. Repair runs (TicketType.GetByID
	// + Inventory.SetTicketTypeMetadata). Second Deduct: returns
	// sold_out so we don't reach the DB INSERT (nil db panic
	// avoidance — INSERT-fail integration coverage lives in slice 4).
	deducter := &fakeDeducter{
		responses: []deductResponse{
			{err: domain.ErrTicketTypeRuntimeMetadataMissing},
			{result: domain.DeductInventoryResult{Accepted: false}, err: nil},
		},
	}
	svc, _, ttRepo, invRepo := newServiceForTest(t, ctrl, deducter)

	gomock.InOrder(
		ttRepo.EXPECT().GetByID(gomock.Any(), ticketTypeID).Return(rebuilt, nil),
		invRepo.EXPECT().SetTicketTypeMetadata(gomock.Any(), gomock.Any()).Return(nil),
	)

	_, err = svc.BookTicket(context.Background(), 1, ticketTypeID, 1)
	assert.ErrorIs(t, err, domain.ErrSoldOut, "post-repair retry returning sold_out surfaces ErrSoldOut")
	assert.Equal(t, 2, deducter.calls, "must call Deduct exactly twice: pre-repair + post-repair retry")
}

func TestService_BookTicket_MetadataStillMissingAfterRepair(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	ticketTypeID := uuid.New()
	rebuilt, err := domain.NewTicketType(ticketTypeID, uuid.New(), "GA", 5000, "usd", 100, nil, nil, nil, "")
	require.NoError(t, err)

	deducter := &fakeDeducter{
		responses: []deductResponse{
			{err: domain.ErrTicketTypeRuntimeMetadataMissing},
			{err: domain.ErrTicketTypeRuntimeMetadataMissing},
		},
	}
	svc, _, ttRepo, invRepo := newServiceForTest(t, ctrl, deducter)

	ttRepo.EXPECT().GetByID(gomock.Any(), ticketTypeID).Return(rebuilt, nil)
	invRepo.EXPECT().SetTicketTypeMetadata(gomock.Any(), gomock.Any()).Return(nil)

	_, err = svc.BookTicket(context.Background(), 1, ticketTypeID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrTicketTypeRuntimeMetadataMissing)
	assert.Contains(t, err.Error(), "still missing after repair", "error message must distinguish post-repair persistent failure from initial transient case")
}

func TestService_BookTicket_RepairFails_TicketTypeNotFound(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	ticketTypeID := uuid.New()
	deducter := &fakeDeducter{
		responses: []deductResponse{{err: domain.ErrTicketTypeRuntimeMetadataMissing}},
	}
	svc, _, ttRepo, _ := newServiceForTest(t, ctrl, deducter)

	ttRepo.EXPECT().GetByID(gomock.Any(), ticketTypeID).Return(domain.TicketType{}, domain.ErrTicketTypeNotFound)

	_, err := svc.BookTicket(context.Background(), 1, ticketTypeID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrTicketTypeNotFound)
	assert.Equal(t, 1, deducter.calls, "Deducter not called again when repair fails at GetByID")
}

func TestService_BookTicket_RepairFails_SetMetadataError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	ticketTypeID := uuid.New()
	rebuilt, err := domain.NewTicketType(ticketTypeID, uuid.New(), "GA", 5000, "usd", 100, nil, nil, nil, "")
	require.NoError(t, err)

	deducter := &fakeDeducter{
		responses: []deductResponse{{err: domain.ErrTicketTypeRuntimeMetadataMissing}},
	}
	svc, _, ttRepo, invRepo := newServiceForTest(t, ctrl, deducter)

	redisFailure := errors.New("redis: write timeout")
	ttRepo.EXPECT().GetByID(gomock.Any(), ticketTypeID).Return(rebuilt, nil)
	invRepo.EXPECT().SetTicketTypeMetadata(gomock.Any(), gomock.Any()).Return(redisFailure)

	_, err = svc.BookTicket(context.Background(), 1, ticketTypeID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, redisFailure, "SetTicketTypeMetadata error must wrap, not replace")
	assert.Equal(t, 1, deducter.calls)
}

// TestService_BookTicket_RevertFailureMasksInvariantSentinel — when
// NewReservation rejects a Lua-supplied price snapshot AND the
// follow-up RevertInventory fails, the returned error must NOT
// match the domain.ErrInvalid* sentinel that would map to 400.
// Otherwise stagehttp.MapBookingError would surface 400 with a
// leaked-Redis-inventory state — a worse outcome than 500. (Codex
// round-3 P2: wraps revertErr, not the primary sentinel.)
//
// The 23505-equivalent case (insertOrder failure) lives in the
// integration suite at synclua_booking_test.go because it requires
// real PG to trigger uq_orders_user_event; the wrapping logic is
// shared, so testing one branch here + the other there is enough.
func TestService_BookTicket_RevertFailureMasksInvariantSentinel(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	ticketTypeID := uuid.New()
	eventID := uuid.New()
	// fakeDeducter returns Accepted=true with AmountCents=0 — the
	// only Lua-reachable shape that trips a NewReservation
	// invariant (price snapshot must be > 0). Real Lua would reject
	// price_cents=0 in `metadata_missing`, but we're testing the
	// Go-side defense-in-depth here.
	deducter := &fakeDeducter{
		responses: []deductResponse{{
			result: domain.DeductInventoryResult{
				Accepted:    true,
				EventID:     eventID,
				AmountCents: 0,
				Currency:    "usd",
			},
		}},
	}
	svc, _, _, invRepo := newServiceForTest(t, ctrl, deducter)

	redisDown := errors.New("redis: connection refused")
	invRepo.EXPECT().
		RevertInventory(gomock.Any(), ticketTypeID, 1, gomock.Any()).
		Return(redisDown)

	_, err := svc.BookTicket(context.Background(), 1, ticketTypeID, 1)
	require.Error(t, err)

	// Critical: the error chain must NOT match the invariant
	// sentinel. If it did, MapBookingError would route to 400
	// while Redis is leaked — surfacing the wrong outcome class.
	assert.False(t, errors.Is(err, domain.ErrInvalidAmountCents),
		"revert-failure error must NOT chain to the primary invariant sentinel; got %v", err)

	// And the chain MUST match the revert error so an operator
	// grepping for the redis cause finds it.
	assert.ErrorIs(t, err, redisDown,
		"revert-failure error must chain to the underlying redis error")

	// The original cause is still surfaced via the message for
	// log triage even though it's not in the error chain.
	assert.Contains(t, err.Error(), "redis revert failed",
		"error message must explicitly call out the revert failure")
}

func TestService_GetOrder_DelegatesToOrderRepo(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	orderID, err := uuid.NewV7()
	require.NoError(t, err)
	wantOrder := domain.ReconstructOrder(
		orderID, 1, uuid.New(), uuid.New(), 1,
		domain.OrderStatusAwaitingPayment, time.Now(),
		time.Now().Add(15*time.Minute), "", 5000, "usd",
	)

	svc, orderRepo, _, _ := newServiceForTest(t, ctrl, &fakeDeducter{})
	orderRepo.EXPECT().GetByID(gomock.Any(), orderID).Return(wantOrder, nil)

	got, err := svc.GetOrder(context.Background(), orderID)
	require.NoError(t, err)
	assert.Equal(t, wantOrder.ID(), got.ID())
}

func TestService_GetBookingHistory_PaginationNormalization(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		page, pageSize int
		wantLimit      int
		wantOffset     int
	}{
		{"defaults_normalize_to_first_page", 0, 0, 10, 0},
		{"page_1_size_50_first_page", 1, 50, 50, 0},
		{"page_3_size_25_offset_50", 3, 25, 25, 50},
		{"negative_page_normalized", -5, 25, 25, 0},
		{"negative_size_normalized", 1, -1, 10, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			svc, orderRepo, _, _ := newServiceForTest(t, ctrl, &fakeDeducter{})

			orderRepo.EXPECT().
				ListOrders(gomock.Any(), tc.wantLimit, tc.wantOffset, gomock.Nil()).
				Return([]domain.Order{}, 0, nil)

			_, _, err := svc.GetBookingHistory(context.Background(), tc.page, tc.pageSize, nil)
			require.NoError(t, err)
		})
	}
}
