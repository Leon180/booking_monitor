package cache

import (
	"context"
	"testing"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

func TestRedisOrderQueue_EnsureGroup(t *testing.T) {
	// Setup Miniredis
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	queue := NewRedisOrderQueue(rdb, nil, nopLogger, &config.Config{App: config.AppConfig{WorkerID: "worker-1"}})

	ctx := context.Background()

	// 1. Create Group (Success)
	err := queue.EnsureGroup(ctx)
	assert.NoError(t, err)

	// Verify Stream and Group Exist
	groups, err := rdb.XInfoGroups(ctx, "orders:stream").Result()
	assert.NoError(t, err)
	assert.Equal(t, 1, len(groups))
	assert.Equal(t, "orders:group", groups[0].Name)

	// 2. Create Group (Already Exists - Idempotent)
	err = queue.EnsureGroup(ctx)
	assert.NoError(t, err)
}

func TestRedisOrderQueue_Subscribe_PELRecovery(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	// No mock needed for happy path
	// Setup Config
	cfg := &config.Config{App: config.AppConfig{WorkerID: "worker-1"}}
	queue := NewRedisOrderQueue(rdb, nil, nopLogger, cfg)
	ctx := mlog.NewContext(context.Background(), nopLogger, "")

	// 1. Create Stream & Group
	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")

	// 2. Add a message and claim it (simulate pending)
	// Add message
	id, _ := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "orders:stream",
		Values: map[string]interface{}{
			"user_id": "1", "event_id": "1", "quantity": "1",
		},
	}).Result()

	// Claim it (ReadGroup)
	_, _ = rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    "orders:group",
		Consumer: "worker-1",
		Streams:  []string{"orders:stream", ">"},
		Count:    1,
	}).Result()

	// 3. Subscribe (Checks PEL first)
	// We use a context with timeout to stop the infinite loop of Subscribe
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	processedCount := 0
	handler := func(ctx context.Context, msg *domain.OrderMessage) error {
		processedCount++
		assert.Equal(t, id, msg.ID)
		assert.Equal(t, 1, msg.UserID)
		return nil
	}

	// 4. Run Subscribe
	err := queue.Subscribe(ctx, handler)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// 5. Verify processed
	assert.Equal(t, 1, processedCount)

	// 6. Verify Acked (Pending List should be empty)
	// Use background context as the previous ctx is cancelled
	pending, err := rdb.XPending(context.Background(), "orders:stream", "orders:group").Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), pending.Count)
}

func TestRedisOrderQueue_ParseMessage_Error(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	queue := NewRedisOrderQueue(rdb, nil, nopLogger, &config.Config{App: config.AppConfig{WorkerID: "worker-1"}})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")

	// Add MALFORMED message (missing fields)
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "orders:stream",
		Values: map[string]interface{}{"foo": "bar"},
	})

	handlerCalled := false
	handler := func(ctx context.Context, msg *domain.OrderMessage) error {
		handlerCalled = true
		return nil
	}

	_ = queue.Subscribe(ctx, handler)

	// Handler should NOT be called for malformed message
	assert.False(t, handlerCalled)

	// Should be moved to DLQ
	len, _ := rdb.XLen(context.Background(), "orders:dlq").Result()
	assert.Equal(t, int64(1), len)
}

// TestRedisOrderQueue_Subscribe_PersistentErrorBailout verifies that
// Subscribe exits with an error (not loops forever) when the underlying
// Redis is durably unreachable. Previously the loop logged + slept + retried
// indefinitely, leaving the process "alive" to k8s while no messages could
// be consumed. The fix: bounded consecutiveErrors counter → error return.
func TestRedisOrderQueue_Subscribe_PersistentErrorBailout(t *testing.T) {
	s := miniredis.RunT(t)

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	queue := NewRedisOrderQueue(rdb, nil, nopLogger, &config.Config{App: config.AppConfig{WorkerID: "worker-1"}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create the stream/group, then close Redis so XReadGroup fails on
	// every iteration. The loop should exit with a wrapped error after
	// maxConsecutiveReadErrors attempts instead of spinning forever.
	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")
	s.Close()

	handlerCalled := false
	handler := func(ctx context.Context, msg *domain.OrderMessage) error {
		handlerCalled = true
		return nil
	}

	err := queue.Subscribe(ctx, handler)

	assert.Error(t, err, "Subscribe must return error after persistent Redis failure")
	assert.Contains(t, err.Error(), "XReadGroup")
	assert.Contains(t, err.Error(), "consecutive errors")
	assert.False(t, handlerCalled, "Handler must not fire while Redis is down")
	// ctx had 2 minutes; the bailout should have occurred well before then.
	// If this assert fails, the loop bailed on ctx.Err() not consecutiveErrors.
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
}
