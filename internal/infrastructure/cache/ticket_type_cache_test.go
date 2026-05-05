package cache

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"
)

// ticketTypeCacheHarness wires miniredis + a gomock TicketTypeRepository.
// The decorator under test sits between callers and the mock; tests assert
// on hit/miss counters, mock call counts, and Redis-side state.
func ticketTypeCacheHarness(t *testing.T, ttl time.Duration) (
	domain.TicketTypeRepository, // the decorator (system under test)
	*mocks.MockTicketTypeRepository, // inner mock
	*miniredis.Miniredis,
	*redis.Client,
) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockRepo := mocks.NewMockTicketTypeRepository(ctrl)

	dec := NewTicketTypeCacheDecorator(mockRepo, rdb, ttl, mlog.NewNop())
	return dec, mockRepo, s, rdb
}

// fixtureTicketType builds a domain.TicketType with arbitrary-but-stable
// values via ReconstructTicketType (skips invariant validation, which is
// what we want — these tests aren't about domain rules, they're about
// the cache layer faithfully round-tripping the aggregate).
func fixtureTicketType(t *testing.T, id uuid.UUID) domain.TicketType {
	t.Helper()
	saleStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	saleEnd := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	perUser := 4
	return domain.ReconstructTicketType(
		id, uuid.New(), "VIP early-bird",
		2000, "usd", 100, 80,
		&saleStart, &saleEnd, &perUser, "VIP-A",
		3,
	)
}

// counterValue reads the labelled cache hit/miss counter at test time.
// Necessary because the registry is process-global — we can't rely on
// "starts at 0" between tests.
func counterValue(t *testing.T, label, kind string) float64 {
	t.Helper()
	switch kind {
	case "hit":
		return testutil.ToFloat64(observability.CacheHitsTotal.WithLabelValues(label))
	case "miss":
		return testutil.ToFloat64(observability.CacheMissesTotal.WithLabelValues(label))
	default:
		t.Fatalf("counterValue: unknown kind %q", kind)
		return 0
	}
}

// TestTicketTypeCache_Miss_PopulatesCache — first GetByID for an id is a
// cache miss → inner is called → result written to Redis with TTL. The
// next GetByID (in TestTicketTypeCache_Hit_BypassesInner) confirms the
// fill is reusable.
func TestTicketTypeCache_Miss_PopulatesCache(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	tt := fixtureTicketType(t, id)

	missesBefore := counterValue(t, cacheLabelTicketType, "miss")

	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(tt, nil).Times(1)

	got, err := dec.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, tt.ID(), got.ID())
	assert.Equal(t, tt.Name(), got.Name())
	assert.Equal(t, tt.PriceCents(), got.PriceCents())
	assert.Equal(t, tt.Currency(), got.Currency())

	// Counter incremented exactly once.
	assert.Equal(t, missesBefore+1, counterValue(t, cacheLabelTicketType, "miss"),
		"miss must register a single Prometheus counter increment")

	// Redis key written.
	key := ticketTypeCacheKey(id)
	val, getErr := s.Get(key)
	require.NoError(t, getErr, "Redis key should be populated after a miss")
	assert.NotEmpty(t, val)

	// TTL set (miniredis tracks TTL).
	ttl := s.TTL(key)
	assert.Greater(t, ttl, time.Duration(0))
	assert.LessOrEqual(t, ttl, 5*time.Minute)
}

// TestTicketTypeCache_Hit_BypassesInner — second GetByID for a cached id
// MUST NOT call the inner repo (gomock fails the test if it does). The
// returned aggregate must reconstruct the same fields.
func TestTicketTypeCache_Hit_BypassesInner(t *testing.T) {
	dec, mockRepo, _, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	tt := fixtureTicketType(t, id)

	// First call — fills cache. Allow ONE inner call.
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(tt, nil).Times(1)
	_, err := dec.GetByID(ctx, id)
	require.NoError(t, err)

	hitsBefore := counterValue(t, cacheLabelTicketType, "hit")

	// Second call — MUST be a hit. gomock would fail if inner is called
	// again (no further EXPECT registered).
	got, err := dec.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, tt.ID(), got.ID())
	assert.Equal(t, tt.Name(), got.Name())
	assert.Equal(t, tt.EventID(), got.EventID())
	assert.Equal(t, tt.PriceCents(), got.PriceCents())
	assert.Equal(t, tt.TotalTickets(), got.TotalTickets())
	assert.Equal(t, tt.AreaLabel(), got.AreaLabel())
	assert.Equal(t, *tt.PerUserLimit(), *got.PerUserLimit())
	assert.True(t, tt.SaleStartsAt().Equal(*got.SaleStartsAt()),
		"SaleStartsAt must round-trip; expected %v, got %v", *tt.SaleStartsAt(), *got.SaleStartsAt())
	assert.True(t, tt.SaleEndsAt().Equal(*got.SaleEndsAt()),
		"SaleEndsAt must round-trip; expected %v, got %v", *tt.SaleEndsAt(), *got.SaleEndsAt())

	// AvailableTickets is DELIBERATELY frozen at 0 after a cache hit
	// (see ticket_type_cache.go::ticketTypeCacheEntry doc — the field
	// is mutable so we omit it from the cache entry; toDomain returns
	// the conservative-safe sentinel 0). Pin the contract so a future
	// regression that re-introduces AvailableTickets to the entry is
	// caught loudly: a caller branching on `> 0` would silently start
	// reading stale data.
	assert.Equal(t, 0, got.AvailableTickets(),
		"cache MUST NOT round-trip AvailableTickets — it's mutable; toDomain returns 0 as the conservative-safe sentinel")

	assert.Equal(t, hitsBefore+1, counterValue(t, cacheLabelTicketType, "hit"),
		"hit must register a single Prometheus counter increment")
}

// TestTicketTypeCache_NotFound_NotCached — ErrTicketTypeNotFound from inner
// MUST surface verbatim AND MUST NOT be cached. A brand-new ticket_type
// Created and immediately GetByID'd would otherwise be invisible for TTL
// minutes (negative-cache stampede protection NOT worth that staleness).
func TestTicketTypeCache_NotFound_NotCached(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()

	// First call returns NotFound. Inner is allowed to be hit.
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(domain.TicketType{}, domain.ErrTicketTypeNotFound).Times(1)

	_, err := dec.GetByID(ctx, id)
	assert.ErrorIs(t, err, domain.ErrTicketTypeNotFound)

	// No Redis entry should exist.
	_, getErr := s.Get(ticketTypeCacheKey(id))
	assert.ErrorIs(t, getErr, miniredis.ErrKeyNotFound,
		"NotFound MUST NOT be cached — would mask a fresh Create+GetByID race")

	// Second call must hit the inner repo again. We register a SECOND
	// expectation; gomock fails the test if it isn't satisfied.
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(domain.TicketType{}, domain.ErrTicketTypeNotFound).Times(1)
	_, err = dec.GetByID(ctx, id)
	assert.ErrorIs(t, err, domain.ErrTicketTypeNotFound)
}

// TestTicketTypeCache_InnerError_NotCached — non-NotFound errors from
// inner (e.g. DB outage) also must NOT be cached. Same semantic as
// NotFound: the next caller should retry against the inner repo.
//
// Pinning a SECOND call against a SECOND inner expectation; without
// this, a regression that cached the error result on the first call
// would let the second call return from cache (mock unsatisfied) AND
// the test would still pass on the Redis-key-absent assertion alone.
// The two-call pattern is the airtight version (silent-failure-hunter
// finding H from review round 1).
func TestTicketTypeCache_InnerError_NotCached(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	dbErr := errors.New("postgres: connection refused")

	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(domain.TicketType{}, dbErr).Times(1)
	_, err := dec.GetByID(ctx, id)
	assert.ErrorIs(t, err, dbErr)

	_, getErr := s.Get(ticketTypeCacheKey(id))
	assert.ErrorIs(t, getErr, miniredis.ErrKeyNotFound)

	// Second call MUST hit the inner repo again — gomock fails the
	// test if the second EXPECT isn't satisfied (i.e. a regression
	// that started caching the error would cause this expectation to
	// remain uncalled).
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(domain.TicketType{}, dbErr).Times(1)
	_, err = dec.GetByID(ctx, id)
	assert.ErrorIs(t, err, dbErr)
}

// TestTicketTypeCache_Delete_InvalidatesEntry — Delete on the decorator
// removes the cache entry so a subsequent GetByID re-queries the inner
// repo. Strictly speaking not load-bearing for D4.1 (only event-creation
// rollback calls Delete, and the booking handler can't reach a deleted
// ticket_type's event_id), but pinning the contract for D8 multi-
// ticket-type lifecycle.
func TestTicketTypeCache_Delete_InvalidatesEntry(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	tt := fixtureTicketType(t, id)

	// Fill cache.
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(tt, nil).Times(1)
	_, err := dec.GetByID(ctx, id)
	require.NoError(t, err)
	require.True(t, s.Exists(ticketTypeCacheKey(id)))

	// Delete via decorator.
	mockRepo.EXPECT().Delete(gomock.Any(), id).Return(nil).Times(1)
	require.NoError(t, dec.Delete(ctx, id))

	// Cache entry GONE.
	assert.False(t, s.Exists(ticketTypeCacheKey(id)),
		"Delete MUST proactively invalidate the cache entry — relying on TTL leaves a stale-data window")
}

// TestTicketTypeCache_Delete_InnerErrorPropagates — when the inner
// Delete fails, we MUST NOT touch the cache entry. The cache entry
// represents a row that still exists in the DB.
func TestTicketTypeCache_Delete_InnerErrorPropagates(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	tt := fixtureTicketType(t, id)
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(tt, nil).Times(1)
	_, err := dec.GetByID(ctx, id)
	require.NoError(t, err)

	dbErr := errors.New("postgres: lock timeout")
	mockRepo.EXPECT().Delete(gomock.Any(), id).Return(dbErr).Times(1)

	err = dec.Delete(ctx, id)
	assert.ErrorIs(t, err, dbErr)
	// Entry preserved — DB still has the row.
	assert.True(t, s.Exists(ticketTypeCacheKey(id)),
		"inner Delete error must NOT invalidate the cache; the row still exists in DB")
}

// TestTicketTypeCache_TTLZero_BypassesCache — a non-positive TTL means
// the decorator passes every GetByID through to the inner repo and
// never writes to Redis. Pinned because the constructor is documented
// to "fail-soft on operational misconfiguration"; a future regression
// that flipped this to "panic at startup" or "still cache with
// effectively no TTL" would silently break operator overrides.
func TestTicketTypeCache_TTLZero_BypassesCache(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 0)
	ctx := context.Background()

	id := uuid.New()
	tt := fixtureTicketType(t, id)

	// TWO calls; both must reach inner because cache is bypassed.
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(tt, nil).Times(2)

	_, err := dec.GetByID(ctx, id)
	require.NoError(t, err)
	_, err = dec.GetByID(ctx, id)
	require.NoError(t, err)

	// No Redis traffic.
	assert.False(t, s.Exists(ticketTypeCacheKey(id)),
		"TTL=0 must skip cache writes entirely")
}

// TestTicketTypeCache_MalformedEntry_TreatedAsMiss — a corrupted /
// version-skewed cache entry must NOT short-circuit; the decorator
// must fall through to the inner repo and overwrite with a fresh
// entry. Without this, a manual `redis-cli SET ticket_type:... bogus`
// would poison the cache until TTL expiry.
func TestTicketTypeCache_MalformedEntry_TreatedAsMiss(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	tt := fixtureTicketType(t, id)

	// Pre-populate the Redis key with garbage (simulates a manual edit
	// or a forwards-incompatible schema change).
	require.NoError(t, s.Set(ticketTypeCacheKey(id), "{not-valid-json}"))

	// Inner MUST be called (cache treats the malformed entry as a miss).
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(tt, nil).Times(1)

	got, err := dec.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, tt.ID(), got.ID())

	// Cache entry should now hold the FRESH entry, not the garbage.
	val, _ := s.Get(ticketTypeCacheKey(id))
	var entry ticketTypeCacheEntry
	require.NoError(t, json.Unmarshal([]byte(val), &entry),
		"a malformed entry must be overwritten with a parseable one")
	assert.Equal(t, ticketTypeCacheEntryVersion, entry.V)
}

// TestTicketTypeCache_VersionSkew_TreatedAsMiss — an entry from a
// PRIOR cache schema version (e.g. _v=0 from a future downgrade) must
// be re-fetched. Same pattern as malformed-JSON, separate test because
// the comparison branch is different (parses OK but rejects on _v).
func TestTicketTypeCache_VersionSkew_TreatedAsMiss(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	tt := fixtureTicketType(t, id)

	// Write a future-version entry that parses but is unsupported.
	staleEntry := map[string]any{
		"_v":   ticketTypeCacheEntryVersion + 1,
		"id":   id.String(),
		"name": "stale",
	}
	stalePayload, err := json.Marshal(staleEntry)
	require.NoError(t, err)
	require.NoError(t, s.Set(ticketTypeCacheKey(id), string(stalePayload)))

	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(tt, nil).Times(1)
	got, err := dec.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, tt.ID(), got.ID())
	assert.Equal(t, "VIP early-bird", got.Name(), "miss-on-version-skew must overwrite with fresh data")
}

// TestTicketTypeCache_RedisGetError_BumpsErrorCounter — when Redis GET
// fails for a real reason (not redis.Nil), the decorator MUST NOT
// inflate the miss counter (that would mask Redis-down as cache-cold)
// and MUST bump the dedicated `cache_errors_total{op="get"}` counter
// instead. Pinned so a future refactor that re-routes the error
// branch through the miss counter is caught loudly (silent-failure-
// hunter finding B from review round 1).
//
// Approach: close miniredis BEFORE the call, so the Redis client
// returns an io error (not redis.Nil). The decorator falls through
// to the inner repo and counter increments.
func TestTicketTypeCache_RedisGetError_BumpsErrorCounter(t *testing.T) {
	dec, mockRepo, s, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	tt := fixtureTicketType(t, id)

	// Inner is reached because the cache GET errored — booking still
	// works, that's the load-bearing fail-soft contract.
	mockRepo.EXPECT().GetByID(gomock.Any(), id).Return(tt, nil).Times(1)

	missesBefore := counterValue(t, cacheLabelTicketType, "miss")
	getErrsBefore := testutil.ToFloat64(observability.CacheErrorsTotal.WithLabelValues(cacheLabelTicketType, "get"))

	// Close Redis right before the call so GET returns an io error.
	s.Close()

	got, err := dec.GetByID(ctx, id)
	require.NoError(t, err, "Redis GET error must NOT surface — fail-soft contract")
	assert.Equal(t, tt.ID(), got.ID())

	// The error path MUST bump cache_errors_total{op="get"} AND MUST
	// NOT bump cache_misses_total — the distinct counters are the
	// operator's signal that "Redis is down" rather than "cache is
	// cold". Conflating them would mask a Redis outage as an organic
	// miss-rate spike on dashboards.
	assert.Equal(t, getErrsBefore+1, testutil.ToFloat64(observability.CacheErrorsTotal.WithLabelValues(cacheLabelTicketType, "get")),
		"Redis GET error must bump cache_errors_total{op=\"get\"}")
	assert.Equal(t, missesBefore, counterValue(t, cacheLabelTicketType, "miss"),
		"Redis GET error MUST NOT inflate the miss rate — that would mask a Redis outage as a cache-cold spike")
}

// TestTicketTypeCache_PassThroughMethods — Create / ListByEventID /
// DecrementTicket / IncrementTicket / SumAvailableByEventID all pass
// through to the inner repo verbatim. This test pins that contract:
// any future change that accidentally caches one of these would break
// correctness (e.g. cached SumAvailableByEventID would render the
// drift detector blind).
func TestTicketTypeCache_PassThroughMethods(t *testing.T) {
	dec, mockRepo, _, _ := ticketTypeCacheHarness(t, 5*time.Minute)
	ctx := context.Background()

	id := uuid.New()
	eventID := uuid.New()
	tt := fixtureTicketType(t, id)

	mockRepo.EXPECT().Create(gomock.Any(), tt).Return(tt, nil).Times(1)
	_, err := dec.Create(ctx, tt)
	require.NoError(t, err)

	mockRepo.EXPECT().ListByEventID(gomock.Any(), eventID).Return([]domain.TicketType{tt}, nil).Times(1)
	list, err := dec.ListByEventID(ctx, eventID)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	mockRepo.EXPECT().DecrementTicket(gomock.Any(), id, 1).Return(nil).Times(1)
	require.NoError(t, dec.DecrementTicket(ctx, id, 1))

	mockRepo.EXPECT().IncrementTicket(gomock.Any(), id, 1).Return(nil).Times(1)
	require.NoError(t, dec.IncrementTicket(ctx, id, 1))

	mockRepo.EXPECT().SumAvailableByEventID(gomock.Any(), eventID).Return(80, nil).Times(1)
	got, err := dec.SumAvailableByEventID(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, 80, got)
}
