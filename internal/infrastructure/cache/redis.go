package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
	_ "embed"
)

// Pre-allocated sentinel errors for hot-path — avoids fmt.Errorf
// interface boxing on every call.
var (
	errDeductScriptNotFound = errors.New("redis: script 'deduct' not found")
	errRevertScriptNotFound = errors.New("redis: script 'revert' not found")
	errUnexpectedLuaResult  = errors.New("redis: unexpected lua result")
)

// argsPool reuses []interface{} slices for Redis Lua script calls.
// Each DeductInventory call needs 3 args; RevertInventory needs 1.
// We pool a 3-element slice (the common case) and sub-slice for smaller calls.
var argsPool = sync.Pool{
	New: func() interface{} {
		s := make([]interface{}, 3)
		return &s
	},
}

// Module provides the Redis client and the cache-backed
// implementations (inventory, order queue, idempotency). It also
// provides the QueueMetrics impl so the metrics provider always
// travels with the queue consumer that needs it — including
// cache.Module without the matching observability provider would
// otherwise fail at fx startup, not at compile time.
var Module = fx.Options(
	fx.Provide(NewRedisClient),
	fx.Provide(NewRedisInventoryRepository),
	fx.Provide(NewRedisOrderQueue),
	fx.Provide(NewRedisIdempotencyRepository),
	fx.Provide(observability.NewQueueMetrics),
)

type redisInventoryRepository struct {
	client  *redis.Client
	scripts map[string]*redis.Script
}

func NewRedisClient(cfg *config.Config, logger *mlog.Logger) *redis.Client {
	redisCfg := cfg.Redis
	opts := &redis.Options{
		Addr:         redisCfg.Addr,
		Password:     redisCfg.Password,
		DB:           redisCfg.DB,
		PoolSize:     redisCfg.PoolSize,
		MinIdleConns: redisCfg.MinIdleConns,
		ReadTimeout:  redisCfg.ReadTimeout,
		WriteTimeout: redisCfg.WriteTimeout,
		PoolTimeout:  redisCfg.PoolTimeout,
	}

	client := redis.NewClient(opts)

	// Verify connection on startup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		logger.Fatal(ctx, "Failed to connect to Redis", tag.Error(err))
	}

	// Scripts are loaded lazily by redis.Script
	logger.Info(ctx, "Connected to Redis successfully",
		mlog.String("addr", redisCfg.Addr),
		mlog.Int("pool_size", redisCfg.PoolSize),
	)
	return client
}

//go:embed lua/deduct.lua
var deductScriptSource string

//go:embed lua/revert.lua
var revertScriptSource string

func NewRedisInventoryRepository(client *redis.Client) domain.InventoryRepository {
	return &redisInventoryRepository{
		client: client,
		scripts: map[string]*redis.Script{
			"deduct": redis.NewScript(deductScriptSource),
			"revert": redis.NewScript(revertScriptSource),
		},
	}
}

// inventoryKeyPrefix builds the canonical Redis key for an event's
// inventory counter — `event:{uuid}:qty`. UUID's String() method
// produces the canonical 36-char form; this matches what the deduct
// / revert Lua scripts pattern-match on KEYS[1].
const inventoryKeyPrefix = "event:"
const inventoryKeySuffix = ":qty"

func inventoryKey(eventID uuid.UUID) string {
	return inventoryKeyPrefix + eventID.String() + inventoryKeySuffix
}

// inventoryTTL is the maximum lifetime of a Redis inventory key. It is
// intentionally long (30 days) so any active event's inventory is
// re-upserted by operational flows (CreateEvent, saga revert, manual
// reset) well before expiry — but orphaned keys from deleted events
// eventually fall off Redis instead of accumulating forever.
//
// Previously the TTL was 0 (never expires), which caused unbounded key
// growth. See action-list item L3.
const inventoryTTL = 30 * 24 * time.Hour

func (r *redisInventoryRepository) SetInventory(ctx context.Context, eventID uuid.UUID, count int) error {
	return r.client.Set(ctx, inventoryKey(eventID), count, inventoryTTL).Err()
}

func (r *redisInventoryRepository) DeductInventory(ctx context.Context, eventID uuid.UUID, userID int, count int) (bool, error) {
	key := inventoryKey(eventID)
	keys := []string{key}

	// Reuse args slice from pool to avoid per-call allocation.
	// The int→interface{} boxing still escapes, but the slice header doesn't.
	// eventID is passed as the canonical UUID string so the Lua script
	// can include it in the produced stream message verbatim.
	argsPtr := argsPool.Get().(*[]interface{})
	args := *argsPtr
	args[0] = count
	args[1] = eventID.String()
	args[2] = userID
	defer argsPool.Put(argsPtr)

	script, ok := r.scripts["deduct"]
	if !ok {
		return false, errDeductScriptNotFound
	}

	res, err := script.Run(ctx, r.client, keys, args...).Int()
	if err != nil {
		return false, err
	}

	switch res {
	case 1:
		return true, nil
	case -1:
		return false, nil // Sold Out
	default:
		return false, fmt.Errorf("redis: unexpected lua result %d: %w", res, errUnexpectedLuaResult)
	}
}

func (r *redisInventoryRepository) RevertInventory(ctx context.Context, eventID uuid.UUID, count int, compensationID string) error {
	keys := []string{inventoryKey(eventID), "saga:reverted:" + compensationID}

	// Reuse pooled args slice (sub-slice to 1 element for revert).
	argsPtr := argsPool.Get().(*[]interface{})
	args := (*argsPtr)[:1]
	args[0] = count
	defer argsPool.Put(argsPtr)

	script, ok := r.scripts["revert"]
	if !ok {
		return errRevertScriptNotFound
	}

	return script.Run(ctx, r.client, keys, args...).Err()
}
