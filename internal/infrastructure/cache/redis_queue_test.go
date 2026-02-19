package cache

import (
	"context"
	"testing"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/pkg/logger"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestRedisOrderQueue_EnsureGroup(t *testing.T) {
	// Setup Miniredis
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := zap.NewNop().Sugar()

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
	nopLogger := zap.NewNop().Sugar()

	// No mock needed for happy path
	// Setup Config
	cfg := &config.Config{App: config.AppConfig{WorkerID: "worker-1"}}
	queue := NewRedisOrderQueue(rdb, nil, nopLogger, cfg)
	ctx := logger.WithCtx(context.Background(), nopLogger)

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
	rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
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
	nopLogger := zap.NewNop().Sugar()

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

	queue.Subscribe(ctx, handler)

	// Handler should NOT be called for malformed message
	assert.False(t, handlerCalled)

	// Should be moved to DLQ
	len, _ := rdb.XLen(context.Background(), "orders:dlq").Result()
	assert.Equal(t, int64(1), len)
}
