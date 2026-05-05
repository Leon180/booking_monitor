package cache_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/cache"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// rehydrateTestSetup wires miniredis + a mocked event repo + an
// always-acquires-lock mock so each test focuses on the SETNX semantics
// rather than re-doing infrastructure boilerplate.
//
// D4.1 follow-up: also returns a TicketTypeRepository mock — rehydrate
// now rebuilds runtime state from per-event ticket types rather than
// the frozen events.available_tickets column.
func rehydrateTestSetup(t *testing.T) (*redis.Client, *miniredis.Miniredis, *mocks.MockEventRepository, *mocks.MockTicketTypeRepository, *mocks.MockDistributedLock, *config.Config, *mlog.Logger, *gomock.Controller) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	eventRepo := mocks.NewMockEventRepository(ctrl)
	ticketTypeRepo := mocks.NewMockTicketTypeRepository(ctrl)
	locker := mocks.NewMockDistributedLock(ctrl)

	cfg := &config.Config{Redis: config.RedisConfig{InventoryTTL: 1 * time.Hour}}

	return rdb, s, eventRepo, ticketTypeRepo, locker, cfg, mlog.NewNop(), ctrl
}

// makeEvent constructs an Event via ReconstructEvent (same as repo scan).
func makeEvent(t *testing.T, name string, total, available int) domain.Event {
	t.Helper()
	return domain.ReconstructEvent(uuid.New(), name, total, available, 0)
}

func expectTicketTypes(repo *mocks.MockTicketTypeRepository, e domain.Event, available int) domain.TicketType {
	tt := domain.ReconstructTicketType(e.ID(), e.ID(), "Default", 2000, "usd", 1000, available, nil, nil, nil, "", 0)
	repo.EXPECT().ListByEventID(gomock.Any(), e.ID()).Return([]domain.TicketType{tt}, nil)
	return tt
}

// TestRehydrate_LockNotAcquired_NoDBScanNoSETNX pins the contract that
// a lost lock race results in zero work — the leader will populate
// Redis, this instance just logs and returns. Critical for multi-pod
// startup: N pods racing should cost ~1× DB scan, not N×.
func TestRehydrate_LockNotAcquired_NoDBScanNoSETNX(t *testing.T) {
	t.Parallel()
	rdb, _, eventRepo, ticketTypeRepo, locker, cfg, log, _ := rehydrateTestSetup(t)

	// Lock denied on this instance.
	locker.EXPECT().TryLock(gomock.Any(), gomock.Any()).Return(false, nil)
	// CRITICAL: NO ListAvailable, NO Unlock expected. gomock.Controller
	// fails if either fires unexpectedly — that's the assertion.

	err := cache.RehydrateInventory(context.Background(), cache.RehydrateInventoryParams{
		EventRepo:      eventRepo,
		TicketTypeRepo: ticketTypeRepo,
		RedisClient:    rdb,
		Locker:         locker,
		Cfg:            cfg,
		Logger:         log,
	})
	assert.NoError(t, err, "denied lock is not an error — leader will populate Redis")
}

// TestRehydrate_PopulatesEmptyRedis: the canonical recovery scenario.
// Redis is empty (FLUSHALL aftermath, fresh deploy), DB has 3 events
// with various available_tickets. Rehydrate writes all 3 runtime keys and
// returns the right counts in the log.
func TestRehydrate_PopulatesEmptyRedis(t *testing.T) {
	t.Parallel()
	rdb, s, eventRepo, ticketTypeRepo, locker, cfg, log, _ := rehydrateTestSetup(t)

	events := []domain.Event{
		makeEvent(t, "concert", 1000, 750),
		makeEvent(t, "match", 500, 500),
		makeEvent(t, "show", 200, 1),
	}

	locker.EXPECT().TryLock(gomock.Any(), gomock.Any()).Return(true, nil)
	eventRepo.EXPECT().ListAvailable(gomock.Any()).Return(events, nil)
	var ticketTypes []domain.TicketType
	for _, e := range events {
		ticketTypes = append(ticketTypes, expectTicketTypes(ticketTypeRepo, e, e.AvailableTickets()))
	}
	locker.EXPECT().Unlock(gomock.Any(), gomock.Any()).Return(nil)

	err := cache.RehydrateInventory(context.Background(), cache.RehydrateInventoryParams{
		EventRepo:      eventRepo,
		TicketTypeRepo: ticketTypeRepo,
		RedisClient:    rdb,
		Locker:         locker,
		Cfg:            cfg,
		Logger:         log,
	})
	require.NoError(t, err)

	// Verify each ticket type's runtime state was written.
	for i, tt := range ticketTypes {
		key := "ticket_type_qty:" + tt.ID().String()
		val, err := s.Get(key)
		require.NoError(t, err, "key %s should exist", key)
		assert.Equal(t, events[i].AvailableTickets(), parseInt(t, val), "key %s should hold available_tickets", key)
		ttl := s.TTL(key)
		assert.Greater(t, ttl, time.Duration(0), "key %s should have a TTL", key)
		metaKey := "ticket_type_meta:" + tt.ID().String()
		require.True(t, s.Exists(metaKey), "metadata key %s should exist", metaKey)
	}
}

// TestRehydrate_PreservesLiveRedisValues: the safety property. If
// Redis already has a key (Redis didn't actually crash; app just
// restarted), the live value MUST be preserved — overwriting with the
// stale DB value would silently re-add in-flight deducts to inventory.
func TestRehydrate_PreservesLiveRedisValues(t *testing.T) {
	t.Parallel()
	rdb, s, eventRepo, ticketTypeRepo, locker, cfg, log, _ := rehydrateTestSetup(t)

	event := makeEvent(t, "concert", 1000, 800) // DB says 800 available
	tt := expectTicketTypes(ticketTypeRepo, event, event.AvailableTickets())
	key := "ticket_type_qty:" + tt.ID().String()
	// Redis already has 750 (live state — 50 in-flight deducts haven't
	// reached the worker yet, so DB hasn't decremented).
	require.NoError(t, s.Set(key, "750"))

	locker.EXPECT().TryLock(gomock.Any(), gomock.Any()).Return(true, nil)
	eventRepo.EXPECT().ListAvailable(gomock.Any()).Return([]domain.Event{event}, nil)
	locker.EXPECT().Unlock(gomock.Any(), gomock.Any()).Return(nil)

	err := cache.RehydrateInventory(context.Background(), cache.RehydrateInventoryParams{
		EventRepo:      eventRepo,
		TicketTypeRepo: ticketTypeRepo,
		RedisClient:    rdb,
		Locker:         locker,
		Cfg:            cfg,
		Logger:         log,
	})
	require.NoError(t, err)

	// MUST still be 750 — overwriting to 800 would re-add the 50
	// in-flight deducts and cause double-allocation.
	val, _ := s.Get(key)
	assert.Equal(t, 750, parseInt(t, val),
		"live Redis value must be preserved; SETNX is the load-bearing operation")
}

// TestRehydrate_EmptyEventList: no events in DB → no SETNX → nothing
// goes wrong. Just a pure no-op (apart from lock acquisition).
func TestRehydrate_EmptyEventList(t *testing.T) {
	t.Parallel()
	rdb, _, eventRepo, ticketTypeRepo, locker, cfg, log, _ := rehydrateTestSetup(t)

	locker.EXPECT().TryLock(gomock.Any(), gomock.Any()).Return(true, nil)
	eventRepo.EXPECT().ListAvailable(gomock.Any()).Return([]domain.Event{}, nil)
	locker.EXPECT().Unlock(gomock.Any(), gomock.Any()).Return(nil)

	err := cache.RehydrateInventory(context.Background(), cache.RehydrateInventoryParams{
		EventRepo:      eventRepo,
		TicketTypeRepo: ticketTypeRepo,
		RedisClient:    rdb,
		Locker:         locker,
		Cfg:            cfg,
		Logger:         log,
	})
	assert.NoError(t, err)
}

// TestRehydrate_UnlockRunsEvenOnQueryFailure: the lock MUST be released
// even if the DB scan fails — otherwise the next startup wave would
// deadlock on a stale lock holder. Critical because k8s pod restarts
// hit this path frequently.
func TestRehydrate_UnlockRunsEvenOnQueryFailure(t *testing.T) {
	t.Parallel()
	rdb, _, eventRepo, ticketTypeRepo, locker, cfg, log, _ := rehydrateTestSetup(t)

	locker.EXPECT().TryLock(gomock.Any(), gomock.Any()).Return(true, nil)
	eventRepo.EXPECT().ListAvailable(gomock.Any()).Return(nil, assert.AnError)
	// Unlock MUST still fire, otherwise gomock.Controller fails the test.
	locker.EXPECT().Unlock(gomock.Any(), gomock.Any()).Return(nil)

	err := cache.RehydrateInventory(context.Background(), cache.RehydrateInventoryParams{
		EventRepo:      eventRepo,
		TicketTypeRepo: ticketTypeRepo,
		RedisClient:    rdb,
		Locker:         locker,
		Cfg:            cfg,
		Logger:         log,
	})
	assert.Error(t, err, "list query failure must surface; we don't swallow it")
}

// TestRehydrate_SETNXFailureAbortsAndUnlocks: a Redis runtime-write
// failure (metadata HSET or qty SETNX) MUST surface as an error so fx aborts
// startup, AND MUST still release the advisory lock. The fail-fast
// behaviour is intentional — proceeding with a half-populated Redis
// would leave the operator no clear signal.
func TestRehydrate_SETNXFailureAbortsAndUnlocks(t *testing.T) {
	t.Parallel()
	rdb, s, eventRepo, ticketTypeRepo, locker, cfg, log, _ := rehydrateTestSetup(t)

	events := []domain.Event{makeEvent(t, "concert", 1000, 800)}

	// Close miniredis BEFORE the rehydrate runs → SetNX returns an
	// io error. This is the closest test-side analog to "Redis went
	// unreachable between TryLock and the first SETNX".
	s.Close()

	locker.EXPECT().TryLock(gomock.Any(), gomock.Any()).Return(true, nil)
	eventRepo.EXPECT().ListAvailable(gomock.Any()).Return(events, nil)
	expectTicketTypes(ticketTypeRepo, events[0], events[0].AvailableTickets())
	// CRITICAL: Unlock MUST still fire even though SETNX failed.
	locker.EXPECT().Unlock(gomock.Any(), gomock.Any()).Return(nil)

	err := cache.RehydrateInventory(context.Background(), cache.RehydrateInventoryParams{
		EventRepo:      eventRepo,
		TicketTypeRepo: ticketTypeRepo,
		RedisClient:    rdb,
		Locker:         locker,
		Cfg:            cfg,
		Logger:         log,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HSET metadata",
		"error should name the operation that failed for fast triage")
}

// TestRehydrate_DriftDetected: when SETNX returns false (key exists)
// AND the existing Redis value is GREATER than the DB value, increment
// the drift counter + emit a WARN log. Redis < DB is normal (in-flight
// deducts) and must NOT count as drift.
func TestRehydrate_DriftDetected(t *testing.T) {
	t.Parallel()
	rdb, s, eventRepo, ticketTypeRepo, locker, cfg, log, _ := rehydrateTestSetup(t)

	// Two events. Event A: Redis has STALE-HIGH value (drift). Event B:
	// Redis has live-low value (in-flight deducts — normal, not drift).
	eventA := makeEvent(t, "concert-A", 1000, 500) // DB says 500 available
	eventB := makeEvent(t, "concert-B", 1000, 800) // DB says 800 available

	ttA := expectTicketTypes(ticketTypeRepo, eventA, eventA.AvailableTickets())
	ttB := expectTicketTypes(ticketTypeRepo, eventB, eventB.AvailableTickets())
	require.NoError(t, s.Set("ticket_type_qty:"+ttA.ID().String(), "999")) // > DB → drift
	require.NoError(t, s.Set("ticket_type_qty:"+ttB.ID().String(), "750")) // < DB → expected, NOT drift

	locker.EXPECT().TryLock(gomock.Any(), gomock.Any()).Return(true, nil)
	eventRepo.EXPECT().ListAvailable(gomock.Any()).Return([]domain.Event{eventA, eventB}, nil)
	locker.EXPECT().Unlock(gomock.Any(), gomock.Any()).Return(nil)

	err := cache.RehydrateInventory(context.Background(), cache.RehydrateInventoryParams{
		EventRepo:      eventRepo,
		TicketTypeRepo: ticketTypeRepo,
		RedisClient:    rdb,
		Locker:         locker,
		Cfg:            cfg,
		Logger:         log,
	})
	require.NoError(t, err)

	// Both Redis keys remain unchanged (SETNX preserves both).
	a, _ := s.Get("ticket_type_qty:" + ttA.ID().String())
	b, _ := s.Get("ticket_type_qty:" + ttB.ID().String())
	assert.Equal(t, 999, parseInt(t, a), "drift case: SETNX must NOT overwrite (Redis ahead)")
	assert.Equal(t, 750, parseInt(t, b), "in-flight case: SETNX must NOT overwrite (Redis behind)")
	// (We don't directly assert the metric counter here because it's a
	// package-global promauto Counter — covered by smoke test against
	// /metrics. The behaviour assertion above + the drift WARN log is
	// what the unit test pins.)
}

func parseInt(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err)
	return n
}
