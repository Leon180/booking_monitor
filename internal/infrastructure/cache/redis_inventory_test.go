package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/infrastructure/config"
)

// B3 inventory-sharding tests. Pins the contract that:
//   - INVENTORY_SHARDS=1 (B3.1 default) is byte-identical to pre-B3
//     single-key behavior — one Redis key per event, one shard 0
//     returned from DeductInventory, RevertInventory rejects bad shard ids.
//   - INVENTORY_SHARDS=N>1 splits inventory across N keys evenly with
//     remainder to shard 0; DeductInventory random-picks + retries on
//     depleted shards; RevertInventory targets the named shard.
//
// Uses miniredis (the in-memory Redis fake) which supports the Lua
// EVAL path our deduct.lua / revert.lua scripts require.

func newInventoryRepoWithShards(t *testing.T, s *miniredis.Miniredis, shards int) *redisInventoryRepository {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cfg := &config.Config{Redis: config.RedisConfig{
		InventoryShards: shards,
		InventoryTTL:    1 * time.Hour,
	}}
	repo := NewRedisInventoryRepository(rdb, cfg).(*redisInventoryRepository)
	return repo
}

// TestSetInventory_N1_OneKey: with N=1, SetInventory writes exactly
// one Redis key (`event:{id}:qty:0`) holding the full count. Pre-B3
// equivalent except for the `:0` suffix.
func TestSetInventory_N1_OneKey(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 1)
	ctx := context.Background()
	eventID := uuid.New()

	require.NoError(t, repo.SetInventory(ctx, eventID, 100))

	// Exactly one key exists, with the expected name + value.
	keys := s.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "event:"+eventID.String()+":qty:0", keys[0])
	val, err := s.Get(keys[0])
	require.NoError(t, err)
	assert.Equal(t, "100", val)
}

// TestSetInventory_N4_EvenSplit: 100 / 4 = 25 each shard; remainder
// is 0 (clean split). All four shard keys exist with value 25.
func TestSetInventory_N4_EvenSplit(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 4)
	ctx := context.Background()
	eventID := uuid.New()

	require.NoError(t, repo.SetInventory(ctx, eventID, 100))

	require.Len(t, s.Keys(), 4)
	for shard := 0; shard < 4; shard++ {
		key := "event:" + eventID.String() + ":qty:" + intToShard(shard)
		val, err := s.Get(key)
		require.NoError(t, err, "shard %d key missing", shard)
		assert.Equal(t, "25", val, "shard %d expected 25, got %s", shard, val)
	}
}

// TestSetInventory_N4_RemainderToShard0: 7 / 4 → shard 0 gets 4
// (1 + 3 remainder), shards 1-3 get 1. The remainder always lands on
// shard 0 — a deliberate choice so a single inventory key always has
// a usable count even at very low total_tickets.
func TestSetInventory_N4_RemainderToShard0(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 4)
	ctx := context.Background()
	eventID := uuid.New()

	require.NoError(t, repo.SetInventory(ctx, eventID, 7))

	expected := []string{"4", "1", "1", "1"} // shard 0 holds the +3 remainder
	for shard, want := range expected {
		key := "event:" + eventID.String() + ":qty:" + intToShard(shard)
		val, err := s.Get(key)
		require.NoError(t, err, "shard %d key missing", shard)
		assert.Equal(t, want, val, "shard %d", shard)
	}
}

// TestDeductInventory_N1_AlwaysShardZero: with N=1, DeductInventory
// always lands on shard 0 (the only shard). Stream message gets
// shard=0 in its payload.
func TestDeductInventory_N1_AlwaysShardZero(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 1)
	ctx := context.Background()
	eventID := uuid.New()
	orderID := uuid.New()

	require.NoError(t, repo.SetInventory(ctx, eventID, 10))

	success, shard, err := repo.DeductInventory(ctx, orderID, eventID, 1, 1)
	require.NoError(t, err)
	assert.True(t, success)
	assert.Equal(t, 0, shard)

	// Inventory decremented.
	val, err := s.Get("event:" + eventID.String() + ":qty:0")
	require.NoError(t, err)
	assert.Equal(t, "9", val)
}

// TestDeductInventory_N4_RandomShardPick: with N=4, DeductInventory
// returns a shard in [0,4). Run many iterations to verify all four
// shards eventually get picked (sanity check for the random shuffle —
// a deterministic-shard regression would always return 0).
func TestDeductInventory_N4_RandomShardPick(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 4)
	ctx := context.Background()
	eventID := uuid.New()

	require.NoError(t, repo.SetInventory(ctx, eventID, 1000))

	picks := make(map[int]int)
	for i := 0; i < 200; i++ {
		orderID := uuid.New()
		success, shard, err := repo.DeductInventory(ctx, orderID, eventID, i+1, 1)
		require.NoError(t, err)
		require.True(t, success)
		picks[shard]++
	}

	// All four shards should have been picked at least once. With 200
	// iterations and uniform random over 4 shards, the probability of
	// any shard being missed is ~(3/4)^200 ≈ 1e-25 — negligibly flaky.
	assert.Len(t, picks, 4, "all 4 shards should be picked in 200 iterations; got %v", picks)
	for shard := 0; shard < 4; shard++ {
		assert.Greater(t, picks[shard], 0, "shard %d never picked", shard)
	}
}

// TestDeductInventory_N4_RetryOnDepletedShard: when one shard is
// fully depleted but others have stock, DeductInventory MUST find
// the stock by retrying on alternate shards. Worst case: hit the
// depleted shard first, then succeed on a second pick.
func TestDeductInventory_N4_RetryOnDepletedShard(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 4)
	ctx := context.Background()
	eventID := uuid.New()

	// 4 shards with 1 ticket each. Drain shard 0 manually.
	require.NoError(t, repo.SetInventory(ctx, eventID, 4))
	require.NoError(t, s.Set("event:"+eventID.String()+":qty:0", "0"))

	// Three more bookings should succeed (they MUST find shards 1, 2, 3
	// via the retry path; if the random pick hit shard 0 first, the
	// repo retries on alternate shards).
	for i := 0; i < 3; i++ {
		success, shard, err := repo.DeductInventory(ctx, uuid.New(), eventID, i+1, 1)
		require.NoError(t, err, "booking %d should succeed via retry", i)
		require.True(t, success, "booking %d marked sold-out incorrectly", i)
		assert.NotEqual(t, 0, shard, "booking %d landed on depleted shard 0; retry didn't kick in", i)
	}
}

// TestDeductInventory_N4_AllShardsDepleted_GlobalSoldOut: when every
// shard returns -1, the repo must return success=false (globally
// sold out) after probing all N shards. Worst-case sold-out cost is
// N round-trips, which is acceptable because sold-out is the cheap
// fast path post-pool-depletion.
func TestDeductInventory_N4_AllShardsDepleted_GlobalSoldOut(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 4)
	ctx := context.Background()
	eventID := uuid.New()

	// All shards drained.
	require.NoError(t, repo.SetInventory(ctx, eventID, 0))

	success, shard, err := repo.DeductInventory(ctx, uuid.New(), eventID, 1, 1)
	require.NoError(t, err)
	assert.False(t, success, "all-shards-empty should produce success=false (global sold-out)")
	assert.Equal(t, 0, shard, "sold-out shard return is 0 by convention")
}

// TestRevertInventory_N1_HitsShardZero: with N=1, RevertInventory at
// shard 0 increments the only key. Pre-B3 byte-identical behavior
// modulo the `:0` suffix on the key name.
func TestRevertInventory_N1_HitsShardZero(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 1)
	ctx := context.Background()
	eventID := uuid.New()

	require.NoError(t, repo.SetInventory(ctx, eventID, 10))
	// Drain to 9 via deduct, then revert.
	_, _, err := repo.DeductInventory(ctx, uuid.New(), eventID, 1, 1)
	require.NoError(t, err)

	require.NoError(t, repo.RevertInventory(ctx, eventID, 0, 1, "compensation-id-1"))
	val, _ := s.Get("event:" + eventID.String() + ":qty:0")
	assert.Equal(t, "10", val, "revert should restore the deducted ticket")
}

// TestRevertInventory_N4_TargetsCorrectShard: a revert at shard 2
// only modifies shard 2 — the other three shards are untouched. This
// pins the shard-aware revert contract that prevents over-restoring
// the wrong shard (which would leave one shard's count inflated above
// its original allocation, breaking the per-shard accounting story).
func TestRevertInventory_N4_TargetsCorrectShard(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 4)
	ctx := context.Background()
	eventID := uuid.New()

	require.NoError(t, repo.SetInventory(ctx, eventID, 100)) // 25 each shard
	// Manually drain shard 2 by 5 (simulate 5 prior deducts).
	require.NoError(t, s.Set("event:"+eventID.String()+":qty:2", "20"))

	// Revert 5 to shard 2.
	require.NoError(t, repo.RevertInventory(ctx, eventID, 2, 5, "compensation-id-2"))

	// Shard 2 should be back to 25; the other shards untouched.
	for shard := 0; shard < 4; shard++ {
		val, err := s.Get("event:" + eventID.String() + ":qty:" + intToShard(shard))
		require.NoError(t, err)
		assert.Equal(t, "25", val, "shard %d should be 25 after revert", shard)
	}
}

// TestRevertInventory_RejectsOutOfRangeShard: a corrupted
// OrderFailedEvent.Shard could lead to reverting a shard outside the
// configured range. Reject loudly with ErrShardOutOfRange so the saga
// compensator can errors.Is-branch on it (operator action: drain
// in-flight reverts before reducing INVENTORY_SHARDS) instead of
// confusing it with a transient Redis fault.
func TestRevertInventory_RejectsOutOfRangeShard(t *testing.T) {
	s := miniredis.RunT(t)
	repo := newInventoryRepoWithShards(t, s, 4)
	ctx := context.Background()
	eventID := uuid.New()

	require.NoError(t, repo.SetInventory(ctx, eventID, 100))

	// shard=4 is one past the valid range [0,4).
	err := repo.RevertInventory(ctx, eventID, 4, 1, "compensation-id-3")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrShardOutOfRange,
		"out-of-range shard MUST surface ErrShardOutOfRange so callers can branch")

	err = repo.RevertInventory(ctx, eventID, -1, 1, "compensation-id-4")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrShardOutOfRange)
}
