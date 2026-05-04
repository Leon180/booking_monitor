package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
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
// Each DeductInventory call needs 8 args (count, event_id, user_id,
// order_id, reserved_until_unix, ticket_type_id, amount_cents,
// currency — D4.1); RevertInventory needs 1. We pool an 8-element
// slice (the common case) and sub-slice for smaller calls.
var argsPool = sync.Pool{
	New: func() interface{} {
		s := make([]interface{}, 8)
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
	// Decorator pattern: the plain Redis repo above is wrapped with
	// hit/miss instrumentation. Mirrors the TracingBookingHandler
	// decoration in api/module.go — single source of truth for how
	// this codebase layers cross-cutting concerns over storage /
	// handler types. Storage code stays observability-unaware.
	fx.Decorate(NewInstrumentedIdempotencyRepository),
	fx.Provide(observability.NewQueueMetrics),
	// Streams collector — XLEN / XPENDING / consumer-lag gauges per
	// scrape. Wired here (not in bootstrap) because the *redis.Client
	// is server-only; bootstrap.CommonModule is shared by all
	// subcommands including ones with no Redis.
	fx.Invoke(registerStreamsCollector),
	// Pool collector — go-redis client connection-pool stats. Sibling
	// to bootstrap.registerDBPoolCollector for *sql.DB. O3.1b: closes
	// the gap left by O3.1a (server-side metrics + app-side cache_*
	// metrics, but no client-side connection-pool view — which is the
	// most common "Redis is slow" cause when server is idle).
	fx.Invoke(registerRedisPoolCollector),
)

// registerStreamsCollector attaches the StreamsCollector to the
// default Prometheus registerer. Same idempotency pattern as
// bootstrap.registerDBPoolCollector — AlreadyRegisteredError is
// success on re-invocation (test re-import, fx restart).
func registerStreamsCollector(client *redis.Client) error {
	if err := prometheus.DefaultRegisterer.Register(observability.NewStreamsCollector(client)); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return fmt.Errorf("registerStreamsCollector: %w", err)
		}
	}
	return nil
}

// registerRedisPoolCollector attaches the RedisPoolCollector to the
// default Prometheus registerer. Same idempotency pattern as
// registerStreamsCollector / bootstrap.registerDBPoolCollector.
func registerRedisPoolCollector(client *redis.Client) error {
	if err := prometheus.DefaultRegisterer.Register(observability.NewRedisPoolCollector(client)); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return fmt.Errorf("registerRedisPoolCollector: %w", err)
		}
	}
	return nil
}

type redisInventoryRepository struct {
	client       *redis.Client
	scripts      map[string]*redis.Script
	inventoryTTL time.Duration
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

func NewRedisInventoryRepository(client *redis.Client, cfg *config.Config) domain.InventoryRepository {
	return &redisInventoryRepository{
		client: client,
		scripts: map[string]*redis.Script{
			"deduct": redis.NewScript(deductScriptSource),
			"revert": redis.NewScript(revertScriptSource),
		},
		inventoryTTL: cfg.Redis.InventoryTTL,
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

// SetInventory writes the inventory counter for an event with the
// configured TTL (`cfg.Redis.InventoryTTL`, default 30d). Long-by-
// default so active events are re-upserted well before expiry by
// operational flows (CreateEvent, saga revert, manual reset);
// orphaned keys from deleted events eventually fall off Redis
// instead of accumulating forever. Previously the TTL was 0 (never
// expires) → unbounded key growth, see action-list L3.
func (r *redisInventoryRepository) SetInventory(ctx context.Context, eventID uuid.UUID, count int) error {
	return r.client.Set(ctx, inventoryKey(eventID), count, r.inventoryTTL).Err()
}

// GetInventory returns the cached qty for an event plus a `found`
// bool that distinguishes "key absent" from "key present with value 0".
// Per the InventoryRepository interface contract, this distinction is
// load-bearing for drift detection — a missing key is `cache_missing`
// (rehydrate didn't run); a present-and-zero key is the legitimate
// sold-out state (or a `cache_low_excess` if DB still says > 0).
//
// `redis.Nil` (key absent) → (0, false, nil).
// Other Redis errors → (0, false, wrapped err); caller treats as
// transient and retries next sweep.
func (r *redisInventoryRepository) GetInventory(ctx context.Context, eventID uuid.UUID) (int, bool, error) {
	val, err := r.client.Get(ctx, inventoryKey(eventID)).Int()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("redisInventoryRepository.GetInventory event=%s: %w", eventID, err)
	}
	return val, true, nil
}

func (r *redisInventoryRepository) DeductInventory(
	ctx context.Context,
	orderID uuid.UUID,
	eventID uuid.UUID,
	ticketTypeID uuid.UUID,
	userID int,
	count int,
	reservedUntil time.Time,
	amountCents int64,
	currency string,
) (bool, error) {
	key := inventoryKey(eventID)
	keys := []string{key}

	// Reuse args slice from pool to avoid per-call allocation.
	// The int→interface{} boxing still escapes, but the slice header doesn't.
	// eventID + orderID + ticketTypeID are passed as canonical UUID strings
	// so the Lua script can include them in the produced stream message
	// verbatim. reservedUntil is converted to UTC unix seconds: Lua's
	// number type is IEEE-754 double which is exact for any int64 ≤ 2^53;
	// unix seconds (~10 digits) and amount_cents (~10 digits even at
	// $90T) are comfortably within that range, so no precision loss. The
	// worker re-parses with time.Unix(_, 0).UTC() / strconv.ParseInt.
	argsPtr := argsPool.Get().(*[]interface{})
	args := *argsPtr
	args[0] = count
	args[1] = eventID.String()
	args[2] = userID
	args[3] = orderID.String()
	args[4] = reservedUntil.UTC().Unix()
	args[5] = ticketTypeID.String()
	args[6] = amountCents
	args[7] = currency
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
