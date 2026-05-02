package cache

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
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
	// ErrShardOutOfRange surfaces when RevertInventory is called with a
	// shard id outside [0, INVENTORY_SHARDS). Distinct sentinel so
	// callers (saga compensator, queue handleFailure) can branch with
	// errors.Is — out-of-range means a corrupted event payload, not a
	// transient Redis fault, so the operational response differs.
	ErrShardOutOfRange = errors.New("redis: shard id out of configured range")
)

// argsPool reuses []interface{} slices for Redis Lua script calls.
// Each DeductInventory call now needs 5 args (count, event_id, user_id,
// order_id, shard); RevertInventory needs 1. We pool a 5-element slice
// (the common case) and sub-slice for smaller calls.
var argsPool = sync.Pool{
	New: func() interface{} {
		s := make([]interface{}, 5)
		return &s
	},
}

// shardPicker holds a per-goroutine *math/rand.Rand (drawn from a
// sync.Pool inside the inventory repo) so Fisher-Yates shuffling for
// shard selection doesn't contend on the global math/rand mutex.
//
// Cap is 16 — far above any realistic INVENTORY_SHARDS value
// (single-host Redis benchmark caps lift around 4-8 shards before
// inter-shard variance dominates). The slice is reused across calls.
//
// We deliberately do NOT use crypto/rand: shard selection wants
// uniform distribution, not unpredictability. crypto/rand is also
// ~50× slower per call and would itself become a hot-path concern.
type shardPicker struct {
	r     *rand.Rand
	order []int
}

// shardPickerSeed seeds each newly-created shardPicker with a
// monotonically incrementing counter mixed with the current time, so
// concurrently-created pickers don't share the same seed (which would
// produce identical shuffle sequences). atomic to be safe under the
// sync.Pool's New() being called concurrently.
var shardPickerSeed atomic.Int64

func newShardPicker() *shardPicker {
	seed := time.Now().UnixNano() ^ shardPickerSeed.Add(1)
	return &shardPicker{
		// gosec G404 silenced: shard selection wants uniform distribution,
		// not unpredictability. crypto/rand is ~50× slower per call and
		// would itself become a hot-path bottleneck. See the type-level
		// comment on shardPicker for the full rationale.
		r:     rand.New(rand.NewSource(seed)), //nolint:gosec // G404: deliberate
		order: make([]int, 0, 16),
	}
}

// shuffleOrder returns a slice of [0..n-1] in random order. Slice is
// owned by the picker and reused across calls — caller MUST NOT
// retain the returned slice past the next shuffleOrder() call on the
// same picker.
func (p *shardPicker) shuffleOrder(n int) []int {
	// Reuse the underlying array; resize the slice header.
	p.order = p.order[:0]
	for i := 0; i < n; i++ {
		p.order = append(p.order, i)
	}
	// Fast-path: N=1 has no permutation work.
	if n == 1 {
		return p.order
	}
	// Fisher-Yates shuffle.
	p.r.Shuffle(len(p.order), func(i, j int) {
		p.order[i], p.order[j] = p.order[j], p.order[i]
	})
	return p.order
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

type redisInventoryRepository struct {
	client       *redis.Client
	scripts      map[string]*redis.Script
	inventoryTTL time.Duration
	// shards is the number of inventory shards per event (B3 sharding).
	// 1 = backwards-compatible single-key behavior (B3.1 default);
	// N>1 = inventory split across N shards (planned B3.2).
	shards int
	// shardRand is a per-repo *math/rand.Rand for shard pick. Pulled
	// from a sync.Pool to avoid global lock contention on the math/rand
	// global source. We're explicitly NOT using crypto/rand because
	// uniform shard distribution is the goal, not unpredictability.
	// (See DeductInventory comment for why deterministic-by-orderID
	// hashing was rejected — it loses retry-on-depleted-shard.)
	shardPickerPool sync.Pool
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
	shards := cfg.Redis.InventoryShards
	if shards < 1 {
		// Validate() in config rejects this, but defend against future
		// constructor callers that bypass config (e.g. tests).
		shards = 1
	}
	return &redisInventoryRepository{
		client: client,
		scripts: map[string]*redis.Script{
			"deduct": redis.NewScript(deductScriptSource),
			"revert": redis.NewScript(revertScriptSource),
		},
		inventoryTTL: cfg.Redis.InventoryTTL,
		shards:       shards,
		shardPickerPool: sync.Pool{
			New: func() interface{} {
				// math/rand.Rand requires a Source; per-rand seeded from
				// time + the picker pool address gives independent
				// streams per goroutine without global lock contention.
				return newShardPicker()
			},
		},
	}
}

// inventoryKey builds the per-shard Redis key for an event's inventory
// counter — `event:{uuid}:qty:{shard}`. With INVENTORY_SHARDS=1 (B3.1
// default), shard is always 0 so the key becomes `event:{uuid}:qty:0`.
//
// Pre-B3 the key was `event:{uuid}:qty` (no shard suffix). The new
// format is uniform across N=1 and N>1 to avoid two coexisting key
// schemes — at the cost of FLUSHALL needed on first deploy of a
// running cluster (acceptable for the simulator; documented for any
// future production path).
const (
	inventoryKeyPrefix = "event:"
	inventoryKeySuffix = ":qty"
)

func inventoryKey(eventID uuid.UUID, shard int) string {
	return inventoryKeyPrefix + eventID.String() + inventoryKeySuffix + ":" + intToShard(shard)
}

// intToShard renders shard ids 0..9 as fast single-byte strings,
// falling back to fmt for double-digit shard counts. Avoids the
// fmt.Sprintf("%d", ...) allocation on every Redis call when N is
// small (the realistic case — N=1, N=4, maybe N=8 ever).
func intToShard(n int) string {
	if n >= 0 && n <= 9 {
		return string(rune('0' + n))
	}
	return fmt.Sprintf("%d", n)
}

// SetInventory writes the inventory counter for an event with the
// configured TTL (`cfg.Redis.InventoryTTL`, default 30d). With N
// shards configured, the count is split evenly with the remainder
// going to shard 0. So 500,000 / 4 shards = 125,000 each. So
// 7 / 4 shards = shard_0=4, shard_1..3=1 (low-tickets edge case).
//
// Long-by-default TTL so active events are re-upserted well before
// expiry by operational flows (CreateEvent, saga revert, manual
// reset); orphaned keys from deleted events eventually fall off
// Redis instead of accumulating forever.
func (r *redisInventoryRepository) SetInventory(ctx context.Context, eventID uuid.UUID, count int) error {
	per := count / r.shards
	remainder := count - per*r.shards
	for shard := 0; shard < r.shards; shard++ {
		shardCount := per
		if shard == 0 {
			shardCount += remainder
		}
		if err := r.client.Set(ctx, inventoryKey(eventID, shard), shardCount, r.inventoryTTL).Err(); err != nil {
			return fmt.Errorf("SetInventory shard=%d: %w", shard, err)
		}
	}
	return nil
}

func (r *redisInventoryRepository) DeductInventory(ctx context.Context, orderID uuid.UUID, eventID uuid.UUID, userID int, count int) (bool, int, error) {
	script, ok := r.scripts["deduct"]
	if !ok {
		return false, 0, errDeductScriptNotFound
	}

	// Pick a randomized starting shard + retry order (Fisher-Yates
	// shuffle of [0..N-1]). Random rather than deterministic so that
	// when shard X depletes, future requests don't all pile onto X
	// and discover its depletion serially — they spread.
	picker := r.shardPickerPool.Get().(*shardPicker)
	defer r.shardPickerPool.Put(picker)
	order := picker.shuffleOrder(r.shards)

	// Reuse args slice from pool. 5 args: count, event_id, user_id,
	// order_id, shard.
	argsPtr := argsPool.Get().(*[]interface{})
	defer argsPool.Put(argsPtr)
	args := *argsPtr
	args[0] = count
	args[1] = eventID.String()
	args[2] = userID
	args[3] = orderID.String()
	// args[4] (shard) set per-iteration below.

	for _, shard := range order {
		args[4] = shard
		keys := []string{inventoryKey(eventID, shard)}

		res, err := script.Run(ctx, r.client, keys, args...).Int()
		if err != nil {
			return false, 0, err
		}

		switch res {
		case 1:
			return true, shard, nil
		case -1:
			// This shard depleted; try the next one.
			continue
		default:
			return false, 0, fmt.Errorf("redis: unexpected lua result %d: %w", res, errUnexpectedLuaResult)
		}
	}

	// Every shard returned -1 → globally sold out.
	return false, 0, nil
}

func (r *redisInventoryRepository) RevertInventory(ctx context.Context, eventID uuid.UUID, shard int, count int, compensationID string) error {
	// Range check is against the *current* deployment's INVENTORY_SHARDS
	// — not the value in effect when the order was deducted. Operational
	// hazard documented for B3.2: if INVENTORY_SHARDS is reduced (e.g.
	// emergency rollback from N=4 → N=1) while OrderFailedEvent messages
	// carrying shard=2/3 are still in flight, this guard rejects them
	// and the saga compensator surfaces the error. Drain in-flight
	// reverts before reducing INVENTORY_SHARDS. For B3.1 with default
	// N=1 this cannot trigger — the only valid shard is 0.
	if shard < 0 || shard >= r.shards {
		return fmt.Errorf("RevertInventory: shard %d out of range [0,%d): %w",
			shard, r.shards, ErrShardOutOfRange)
	}

	keys := []string{inventoryKey(eventID, shard), "saga:reverted:" + compensationID}

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
