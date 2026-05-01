package event_test

import (
	"context"
	"errors"
	"testing"

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

// Tests cover event.service.CreateEvent across:
//   - domain factory invariants (empty name, zero total_tickets)
//   - DB Create error  → wrapped, no Redis call
//   - Redis SetInventory error + Delete success → wrapped Redis error
//   - Redis SetInventory error + Delete fail → "compensation failed" surfaced
//   - happy path → both DB row and Redis hot inventory installed
//
// The compensation logic is the load-bearing part of this service: a
// dangling DB row with no Redis inventory is permanently unsellable
// (booking hot path reads from Redis), so SetInventory failure must
// either succeed-with-rollback or fail-loud. Both branches are covered.

func eventServiceHarness(t *testing.T) (appevent.Service, *mocks.MockEventRepository, *mocks.MockInventoryRepository) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockEventRepository(ctrl)
	inv := mocks.NewMockInventoryRepository(ctrl)
	svc := appevent.NewService(repo, inv, mlog.NewNop())
	return svc, repo, inv
}

// TestCreateEvent_InvariantFailure_EmptyName: domain.NewEvent rejects
// empty name with ErrInvalidEventName before ever touching the repo.
func TestCreateEvent_InvariantFailure_EmptyName(t *testing.T) {
	t.Parallel()
	svc, _, _ := eventServiceHarness(t)

	_, err := svc.CreateEvent(context.Background(), "", 100)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidEventName)
}

// TestCreateEvent_InvariantFailure_ZeroTickets: total_tickets must be
// positive; zero or negative → ErrInvalidTotalTickets.
func TestCreateEvent_InvariantFailure_ZeroTickets(t *testing.T) {
	t.Parallel()
	svc, _, _ := eventServiceHarness(t)

	_, err := svc.CreateEvent(context.Background(), "Concert", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidTotalTickets)
}

// TestCreateEvent_DBCreateError: domain factory passes but the repo
// Create fails (DB unreachable, constraint violation, etc.). Service
// must NOT call SetInventory — there is no event row to attach
// inventory to.
func TestCreateEvent_DBCreateError(t *testing.T) {
	t.Parallel()
	svc, repo, _ := eventServiceHarness(t)

	dbErr := errors.New("postgres: relation \"events\" does not exist")
	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.Event{}, dbErr)
	// inv.SetInventory NOT expected.

	_, err := svc.CreateEvent(context.Background(), "Concert", 100)
	require.Error(t, err)
	assert.ErrorIs(t, err, dbErr)
}

// TestCreateEvent_RedisFails_DeleteCompensates: DB row created, Redis
// SetInventory fails, the Delete-back compensation succeeds. Service
// returns the Redis error so callers know the operation failed; the
// DB is left clean so a retry can re-Create.
func TestCreateEvent_RedisFails_DeleteCompensates(t *testing.T) {
	t.Parallel()
	svc, repo, inv := eventServiceHarness(t)

	created := reconstructEvent(t)
	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(created, nil)

	redisErr := errors.New("redis: connection refused")
	inv.EXPECT().SetInventory(gomock.Any(), created.ID(), 100).Return(redisErr)
	repo.EXPECT().Delete(gomock.Any(), created.ID()).Return(nil)

	_, err := svc.CreateEvent(context.Background(), "Concert", 100)
	require.Error(t, err)
	assert.ErrorIs(t, err, redisErr)
}

// TestCreateEvent_RedisFails_DeleteAlsoFails: dangling-row scenario.
// DB row exists with no Redis inventory and Delete-back ALSO fails.
// Service surfaces both errors so an operator can manually reconcile.
// This is the worst-case path; the service intentionally surfaces it
// loudly rather than swallowing.
func TestCreateEvent_RedisFails_DeleteAlsoFails(t *testing.T) {
	t.Parallel()
	svc, repo, inv := eventServiceHarness(t)

	created := reconstructEvent(t)
	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(created, nil)

	redisErr := errors.New("redis: max clients reached")
	deleteErr := errors.New("postgres: deadlock detected")
	inv.EXPECT().SetInventory(gomock.Any(), created.ID(), 100).Return(redisErr)
	repo.EXPECT().Delete(gomock.Any(), created.ID()).Return(deleteErr)

	_, err := svc.CreateEvent(context.Background(), "Concert", 100)
	require.Error(t, err)
	// The error chain wraps deleteErr (the load-bearing failure) with
	// the redisErr text included for the operator. Verify the wrapped
	// chain includes BOTH so manual recon has the full picture.
	assert.ErrorIs(t, err, deleteErr)
	assert.Contains(t, err.Error(), "redis")
}

// TestCreateEvent_HappyPath: both DB and Redis sides succeed; service
// returns the rehydrated event from the repo's Create result.
func TestCreateEvent_HappyPath(t *testing.T) {
	t.Parallel()
	svc, repo, inv := eventServiceHarness(t)

	created := reconstructEvent(t)
	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(created, nil)
	inv.EXPECT().SetInventory(gomock.Any(), created.ID(), 100).Return(nil)
	// Delete NOT expected — happy path.

	got, err := svc.CreateEvent(context.Background(), "Concert", 100)
	require.NoError(t, err)
	assert.Equal(t, created.ID(), got.ID())
	assert.Equal(t, "Concert", got.Name())
}
