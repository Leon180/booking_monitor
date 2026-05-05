package event_test

import (
	"context"
	"errors"
	"testing"

	"booking_monitor/internal/application"
	appevent "booking_monitor/internal/application/event"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// reconstructEvent builds a persisted-shape domain.Event for tests
// where we want to control the id without going through NewEvent's
// uuid.NewV7() randomness.
func reconstructEvent(t *testing.T) domain.Event {
	t.Helper()
	return domain.ReconstructEvent(uuid.New(), "Concert", 100, 100, 0)
}

// reconstructTicketType builds a persisted-shape domain.TicketType for
// the UoW closure's TicketType.Create return. The eventID + price match
// the inputs threaded through the CreateEvent call so the round-trip
// assertions can pin both ends.
func reconstructTicketType(t *testing.T, eventID uuid.UUID, priceCents int64, currency string, totalTickets int) domain.TicketType {
	t.Helper()
	return domain.ReconstructTicketType(
		uuid.New(), eventID, "Default", priceCents, currency,
		totalTickets, totalTickets, nil, nil, nil, "", 0,
	)
}

// Tests cover event.service.CreateEvent across:
//   - domain factory invariants (empty name, zero total_tickets, bad
//     price/currency surfaced via NewTicketType)
//   - UoW commit failure → wrapped, no Redis call
//   - Redis SetTicketTypeRuntime failure + compensation success → wrapped
//     Redis error, both rows deleted
//   - Redis SetTicketTypeRuntime failure + compensation failure → "compensation
//     failed" surfaced with both errors for manual recon
//   - happy path → both DB rows + Redis hot inventory installed
//
// The compensation path is the load-bearing part of this service: a
// dangling DB row pair with no Redis inventory makes the event
// permanently unsellable. Redis runtime-write failure must either succeed-
// with-rollback (delete both rows) or fail-loud. Both branches are
// covered.

// uowDoSucceeds returns a func that mimics application.UnitOfWork.Do
// committing the closure successfully. The closure is invoked with a
// *application.Repositories whose Event + TicketType are the supplied
// mocks.
func uowDoSucceeds(repos *application.Repositories) func(ctx context.Context, fn func(*application.Repositories) error) error {
	return func(ctx context.Context, fn func(*application.Repositories) error) error {
		return fn(repos)
	}
}

// eventServiceHarness wires the mocks. Note that NewService no longer
// takes standalone EventRepository / TicketTypeRepository — every D4.1
// access goes through the UoW closure (`repos.Event` / `repos.TicketType`).
// Tests still need the mocked repos so we can register expectations on
// them; they're handed to the test code via the return tuple but
// passed into the service ONLY through application.Repositories
// inside the UoW closure body.
func eventServiceHarness(t *testing.T) (
	appevent.Service,
	*mocks.MockUnitOfWork,
	*mocks.MockEventRepository,
	*mocks.MockTicketTypeRepository,
	*mocks.MockInventoryRepository,
) {
	t.Helper()
	ctrl := gomock.NewController(t)
	uow := mocks.NewMockUnitOfWork(ctrl)
	repo := mocks.NewMockEventRepository(ctrl)
	tt := mocks.NewMockTicketTypeRepository(ctrl)
	inv := mocks.NewMockInventoryRepository(ctrl)
	svc := appevent.NewService(uow, inv, mlog.NewNop())
	return svc, uow, repo, tt, inv
}

func TestCreateEvent_InvariantFailure_EmptyName(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := eventServiceHarness(t)

	_, err := svc.CreateEvent(context.Background(), "", 100, 2000, "usd")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidEventName)
}

func TestCreateEvent_InvariantFailure_ZeroTickets(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := eventServiceHarness(t)

	_, err := svc.CreateEvent(context.Background(), "Concert", 0, 2000, "usd")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidTotalTickets)
}

// TestCreateEvent_InvariantFailure_BadPrice: the ticket_type factory
// runs after NewEvent succeeds. A non-positive price short-circuits
// before the UoW opens.
func TestCreateEvent_InvariantFailure_BadPrice(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := eventServiceHarness(t)

	_, err := svc.CreateEvent(context.Background(), "Concert", 100, 0, "usd")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidTicketTypePrice)
}

// TestCreateEvent_InvariantFailure_BadCurrency: same as above for
// non-3-letter currency.
func TestCreateEvent_InvariantFailure_BadCurrency(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := eventServiceHarness(t)

	_, err := svc.CreateEvent(context.Background(), "Concert", 100, 2000, "USDD")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidTicketTypeCurrency)
}

// TestCreateEvent_UoWError: the UoW Do call returns an error (e.g. DB
// constraint violation inside the closure, or a tx-begin failure).
// Service must NOT call SetTicketTypeRuntime.
func TestCreateEvent_UoWError(t *testing.T) {
	t.Parallel()
	svc, uow, _, _, _ := eventServiceHarness(t)

	dbErr := errors.New("postgres: deadlock detected")
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).Return(dbErr)
	// inv.SetTicketTypeRuntime NOT expected.

	_, err := svc.CreateEvent(context.Background(), "Concert", 100, 2000, "usd")
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr)
}

// TestCreateEvent_RedisFails_CompensationSucceeds: the UoW commits
// (event + ticket_type both inserted), Redis runtime write fails, the
// compensation UoW runs and deletes both rows. Service surfaces the
// Redis error so callers know the operation failed; the DB is left
// clean for retry.
//
// Uses gomock.InOrder for the two Do calls (NOT .Times(2) on a single
// expectation) so the test pins the actual call sequence:
// first Do = Create+Create closure, second Do = Delete+Delete closure.
// A regression that swapped the order would otherwise pass under
// .Times(2) because the same DoAndReturn matches both calls.
func TestCreateEvent_RedisFails_CompensationSucceeds(t *testing.T) {
	t.Parallel()
	svc, uow, repo, tt, inv := eventServiceHarness(t)

	created := reconstructEvent(t)
	createdTT := reconstructTicketType(t, created.ID(), 2000, "usd", 100)

	repos := &application.Repositories{Event: repo, TicketType: tt}
	gomock.InOrder(
		// First Do: the original tx — Create + Create.
		uow.EXPECT().Do(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, fn func(*application.Repositories) error) error {
				repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(created, nil)
				tt.EXPECT().Create(gomock.Any(), gomock.Any()).Return(createdTT, nil)
				return fn(repos)
			}),
		// Second Do: the compensation tx — TicketType.Delete + Event.Delete.
		// This is the load-bearing assertion: a regression that calls
		// Event.Delete BEFORE TicketType.Delete (or skips one) would
		// fail the InOrder + per-call expectation match here.
		uow.EXPECT().Do(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, fn func(*application.Repositories) error) error {
				gomock.InOrder(
					tt.EXPECT().Delete(gomock.Any(), createdTT.ID()).Return(nil),
					repo.EXPECT().Delete(gomock.Any(), created.ID()).Return(nil),
				)
				return fn(repos)
			}),
	)

	redisErr := errors.New("redis: connection refused")
	inv.EXPECT().SetTicketTypeRuntime(gomock.Any(), createdTT).Return(redisErr)

	_, err := svc.CreateEvent(context.Background(), "Concert", 100, 2000, "usd")
	require.Error(t, err)
	assert.ErrorIs(t, err, redisErr)
}

// TestCreateEvent_RedisFails_CompensationAlsoFails: dangling-rows
// scenario. The compensation UoW also fails (e.g. DB connection lost
// after Redis went down). Service surfaces BOTH errors via
// `errors.Join` so a future caller can `errors.Is`-branch against
// EITHER sentinel from the same chain.
func TestCreateEvent_RedisFails_CompensationAlsoFails(t *testing.T) {
	t.Parallel()
	svc, uow, repo, tt, inv := eventServiceHarness(t)

	created := reconstructEvent(t)
	createdTT := reconstructTicketType(t, created.ID(), 2000, "usd", 100)

	redisErr := errors.New("redis: max clients reached")
	compensationErr := errors.New("postgres: connection lost during compensation")

	// Initial Do succeeds (event + ticket_type rows committed).
	repos := &application.Repositories{Event: repo, TicketType: tt}
	gomock.InOrder(
		uow.EXPECT().Do(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, fn func(*application.Repositories) error) error {
				repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(created, nil)
				tt.EXPECT().Create(gomock.Any(), gomock.Any()).Return(createdTT, nil)
				return fn(repos)
			}),
		// Compensation Do fails.
		uow.EXPECT().Do(gomock.Any(), gomock.Any()).Return(compensationErr),
	)

	inv.EXPECT().SetTicketTypeRuntime(gomock.Any(), createdTT).Return(redisErr)

	_, err := svc.CreateEvent(context.Background(), "Concert", 100, 2000, "usd")
	require.Error(t, err)
	// errors.Join preserves both branches in the chain — `errors.Is`
	// against EITHER sentinel must match. This is the contract a
	// future caller (e.g. a circuit-breaker middleware checking for
	// redis-specific failures) needs.
	assert.True(t, errors.Is(err, redisErr),
		"errors.Is must match the Redis sentinel — the joined chain must preserve it for circuit-breaker / retry-policy callers")
	assert.True(t, errors.Is(err, compensationErr),
		"errors.Is must match the compensation sentinel — operators searching for the load-bearing failure need a typed branch, not string-match")
}

// TestCreateEvent_HappyPath: UoW commits + Redis runtime write
// succeeds; service returns the rehydrated event AND the default
// ticket_type so the API layer can echo it in the response.
func TestCreateEvent_HappyPath(t *testing.T) {
	t.Parallel()
	svc, uow, repo, tt, inv := eventServiceHarness(t)

	created := reconstructEvent(t)
	createdTT := reconstructTicketType(t, created.ID(), 2000, "usd", 100)

	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(created, nil)
	tt.EXPECT().Create(gomock.Any(), gomock.Any()).Return(createdTT, nil)
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(uowDoSucceeds(&application.Repositories{Event: repo, TicketType: tt}))

	inv.EXPECT().SetTicketTypeRuntime(gomock.Any(), createdTT).Return(nil)

	got, err := svc.CreateEvent(context.Background(), "Concert", 100, 2000, "usd")
	require.NoError(t, err)
	assert.Equal(t, created.ID(), got.Event.ID())
	assert.Equal(t, "Concert", got.Event.Name())
	require.Len(t, got.TicketTypes, 1, "happy path returns exactly one default ticket_type")
	assert.Equal(t, createdTT.ID(), got.TicketTypes[0].ID())
	assert.Equal(t, int64(2000), got.TicketTypes[0].PriceCents())
	assert.Equal(t, "usd", got.TicketTypes[0].Currency())
}

// TestCreateEvent_CurrencyNormalisedToLowercase: the domain factory
// lowercases currency before persistence; the service surfaces the
// normalised value back to the caller.
func TestCreateEvent_CurrencyNormalisedToLowercase(t *testing.T) {
	t.Parallel()
	svc, uow, repo, tt, inv := eventServiceHarness(t)

	created := reconstructEvent(t)
	createdTT := reconstructTicketType(t, created.ID(), 5000, "twd", 50)

	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(created, nil)
	tt.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in domain.TicketType) (domain.TicketType, error) {
			// Currency must be lowercase BEFORE the persistence layer
			// sees it (NormalizeCurrency runs in the domain factory).
			assert.Equal(t, "twd", in.Currency(), "domain factory must lowercase currency before reaching the persistence layer")
			return createdTT, nil
		})
	uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(uowDoSucceeds(&application.Repositories{Event: repo, TicketType: tt}))
	inv.EXPECT().SetTicketTypeRuntime(gomock.Any(), createdTT).Return(nil)

	// Caller passes uppercase "TWD"; service should normalise.
	_, err := svc.CreateEvent(context.Background(), "Concert", 50, 5000, "TWD")
	require.NoError(t, err)
}
