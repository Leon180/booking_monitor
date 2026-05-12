package cache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

// Lua-call arg pools — separate per script so a future schema change
// to one path can't accidentally leak stale slot values to another.
// Pre-D4.1 a single 8-element pool was shared via sub-slicing for the
// 1-arg revert path; that worked, but it was a latent edit-trap (a
// future contributor adding a 9th arg to deduct without re-auditing
// revert's [:1] sub-slice would silently send stale data). Splitting
// the pools makes the per-script arg shape impossible to confuse.

// deductArgsPool sizes the pooled slice to match deduct.lua's ARGV
// count exactly (5: count, user_id, order_id, reserved_until_unix,
// ticket_type_id).
var deductArgsPool = sync.Pool{
	New: func() interface{} {
		s := make([]interface{}, 5)
		return &s
	},
}

// revertArgsPool is a 1-element slice for revert.lua's single ARGV
// (count). Separate from deductArgsPool so the two pools never share
// a backing array.
var revertArgsPool = sync.Pool{
	New: func() interface{} {
		s := make([]interface{}, 1)
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

//go:embed lua/deduct_no_xadd.lua
var deductNoXaddScriptSource string

//go:embed lua/revert.lua
var revertScriptSource string

// NewRedisInventoryRepository returns the standard InventoryRepository
// used by Stages 2-4 (deduct.lua includes XADD orders:stream).
//
// Stage 5 binary should call NewStage5RedisInventoryRepository instead
// (or upcast via the Stage5InventoryRepository interface) to get the
// no-XADD Lua variant for Damai-aligned durable Kafka intake.
func NewRedisInventoryRepository(client *redis.Client, cfg *config.Config) domain.InventoryRepository {
	return newRedisInventoryRepository(client, cfg)
}

// NewStage5RedisInventoryRepository returns the extended interface that
// also exposes DeductInventoryNoStream. Used by cmd/booking-cli-stage5.
func NewStage5RedisInventoryRepository(client *redis.Client, cfg *config.Config) domain.Stage5InventoryRepository {
	return newRedisInventoryRepository(client, cfg)
}

func newRedisInventoryRepository(client *redis.Client, cfg *config.Config) *redisInventoryRepository {
	return &redisInventoryRepository{
		client: client,
		scripts: map[string]*redis.Script{
			"deduct":          redis.NewScript(deductScriptSource),
			"deduct_no_xadd":  redis.NewScript(deductNoXaddScriptSource),
			"revert":          redis.NewScript(revertScriptSource),
		},
		inventoryTTL: cfg.Redis.InventoryTTL,
	}
}

// Runtime key namespaces for booking hot-path state.
//
// Split rather than reusing one `ticket_type:*` prefix:
//   - metadata invalidation must NEVER sweep qty keys
//   - qty is mutable hot state; metadata is immutable booking snapshot
const inventoryKeyPrefix = "ticket_type_qty:"
const inventoryKeySuffix = ":qty"
const ticketTypeMetaKeyPrefix = "ticket_type_meta:"

const (
	ticketTypeMetaFieldEventID    = "event_id"
	ticketTypeMetaFieldPriceCents = "price_cents"
	ticketTypeMetaFieldCurrency   = "currency"
)

func inventoryKey(ticketTypeID uuid.UUID) string {
	return inventoryKeyPrefix + ticketTypeID.String()
}

func ticketTypeMetaKey(ticketTypeID uuid.UUID) string {
	return ticketTypeMetaKeyPrefix + ticketTypeID.String()
}

func ticketTypeMetaValues(ticketType domain.TicketType) map[string]interface{} {
	return map[string]interface{}{
		ticketTypeMetaFieldEventID:    ticketType.EventID().String(),
		ticketTypeMetaFieldPriceCents: ticketType.PriceCents(),
		ticketTypeMetaFieldCurrency:   ticketType.Currency(),
	}
}

// SetTicketTypeRuntime seeds both hot-path runtime keys for a freshly
// created ticket type.
func (r *redisInventoryRepository) SetTicketTypeRuntime(ctx context.Context, ticketType domain.TicketType) error {
	_, err := r.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, ticketTypeMetaKey(ticketType.ID()), ticketTypeMetaValues(ticketType))
		pipe.Expire(ctx, ticketTypeMetaKey(ticketType.ID()), r.inventoryTTL)
		pipe.Set(ctx, inventoryKey(ticketType.ID()), ticketType.AvailableTickets(), r.inventoryTTL)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redisInventoryRepository.SetTicketTypeRuntime ticket_type=%s: %w", ticketType.ID(), err)
	}
	return nil
}

// SetTicketTypeMetadata refreshes ONLY the immutable metadata key.
func (r *redisInventoryRepository) SetTicketTypeMetadata(ctx context.Context, ticketType domain.TicketType) error {
	_, err := r.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, ticketTypeMetaKey(ticketType.ID()), ticketTypeMetaValues(ticketType))
		pipe.Expire(ctx, ticketTypeMetaKey(ticketType.ID()), r.inventoryTTL)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redisInventoryRepository.SetTicketTypeMetadata ticket_type=%s: %w", ticketType.ID(), err)
	}
	return nil
}

// DeleteTicketTypeRuntime removes both the metadata and qty keys.
func (r *redisInventoryRepository) DeleteTicketTypeRuntime(ctx context.Context, ticketTypeID uuid.UUID) error {
	if err := r.client.Del(ctx, ticketTypeMetaKey(ticketTypeID), inventoryKey(ticketTypeID)).Err(); err != nil {
		return fmt.Errorf("redisInventoryRepository.DeleteTicketTypeRuntime ticket_type=%s: %w", ticketTypeID, err)
	}
	return nil
}

// GetInventory returns the cached qty for a ticket type plus a `found`
// bool that distinguishes "key absent" from "key present with value 0".
// Per the InventoryRepository interface contract, this distinction is
// load-bearing for drift detection — a missing key is `cache_missing`
// (rehydrate didn't run); a present-and-zero key is the legitimate
// sold-out state (or a `cache_low_excess` if DB still says > 0).
//
// `redis.Nil` (key absent) → (0, false, nil).
// Other Redis errors → (0, false, wrapped err); caller treats as
// transient and retries next sweep.
func (r *redisInventoryRepository) GetInventory(ctx context.Context, ticketTypeID uuid.UUID) (int, bool, error) {
	val, err := r.client.Get(ctx, inventoryKey(ticketTypeID)).Int()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("redisInventoryRepository.GetInventory ticket_type=%s: %w", ticketTypeID, err)
	}
	return val, true, nil
}

func (r *redisInventoryRepository) DeductInventory(
	ctx context.Context,
	orderID uuid.UUID,
	ticketTypeID uuid.UUID,
	userID int,
	count int,
	reservedUntil time.Time,
) (domain.DeductInventoryResult, error) {
	keys := []string{inventoryKey(ticketTypeID), ticketTypeMetaKey(ticketTypeID)}

	// Reuse args slice from pool to avoid per-call allocation.
	// The int→interface{} boxing still escapes, but the slice header doesn't.
	// orderID + ticketTypeID are passed as canonical UUID strings so the
	// Lua script can include them in the produced stream message verbatim.
	// reservedUntil is converted to UTC unix seconds before the call.
	//
	// Wire-encoding note: go-redis encodes integer arguments (`count`,
	// `userID`, `reservedUntil` unix seconds) as RESP
	// integer frames (`:N\r\n`) — NOT bulk strings. Strings (UUID
	// stringifications) go as RESP bulk strings (`$N\r\n…`).
	// Either way, **Redis presents every ARGV to the Lua script as a
	// Lua string** (this is the RESP→Lua conversion contract; numbers
	// are stringified by Redis itself). The deduct.lua script doesn't
	// call `tonumber()` on user_id / order_id — only on
	// `count`, where DECRBY itself parses the string. The consumer side
	// `parseMessage` re-parses each field with the matching `strconv.*`
	// call. No floating-point conversion happens at any step, so the
	// fields round-trip bit-exact for the ids and timestamps we pass in.
	argsPtr := deductArgsPool.Get().(*[]interface{})
	args := *argsPtr
	args[0] = count
	args[1] = userID
	args[2] = orderID.String()
	args[3] = reservedUntil.UTC().Unix()
	args[4] = ticketTypeID.String()
	defer deductArgsPool.Put(argsPtr)

	script, ok := r.scripts["deduct"]
	if !ok {
		return domain.DeductInventoryResult{}, errDeductScriptNotFound
	}

	raw, err := script.Run(ctx, r.client, keys, args...).Result()
	if err != nil {
		return domain.DeductInventoryResult{}, err
	}

	res, ok := raw.([]interface{})
	if !ok || len(res) == 0 {
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua result %T: %w", raw, errUnexpectedLuaResult)
	}

	status, ok := res[0].(string)
	if !ok {
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua status %T: %w", res[0], errUnexpectedLuaResult)
	}

	switch status {
	case "ok":
		if len(res) != 4 {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected ok payload len=%d: %w", len(res), errUnexpectedLuaResult)
		}
		eventIDStr, ok1 := res[1].(string)
		amountCentsStr, ok2 := res[2].(string)
		currency, ok3 := res[3].(string)
		if !ok1 || !ok2 || !ok3 {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: malformed ok payload: %w", errUnexpectedLuaResult)
		}
		eventID, parseEventErr := uuid.Parse(eventIDStr)
		if parseEventErr != nil {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: parse event_id %q: %w", eventIDStr, parseEventErr)
		}
		amountCents, parseAmountErr := strconv.ParseInt(amountCentsStr, 10, 64)
		if parseAmountErr != nil {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: parse amount_cents %q: %w", amountCentsStr, parseAmountErr)
		}
		return domain.DeductInventoryResult{
			Accepted:    true,
			EventID:     eventID,
			AmountCents: amountCents,
			Currency:    currency,
		}, nil
	case "sold_out":
		return domain.DeductInventoryResult{Accepted: false}, nil
	case "metadata_missing":
		return domain.DeductInventoryResult{}, domain.ErrTicketTypeRuntimeMetadataMissing
	default:
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua status %q: %w", status, errUnexpectedLuaResult)
	}
}

// DeductInventoryNoStream is the Stage 5 variant of DeductInventory.
// Identical atomicity semantics (DECRBY guarded by availability +
// HMGET booking metadata + amount_cents computation) but DOES NOT
// XADD to orders:stream. The caller is responsible for publishing
// the booking message to a durable queue (Stage 5 uses Kafka with
// acks=all) and for calling RevertInventory on publish failure.
//
// Result parsing is intentionally duplicated from DeductInventory
// rather than extracted — the two paths diverge in `args` shape and
// reading them side-by-side keeps the script→Go contract obvious
// when changing either. See `deduct_no_xadd.lua` for the Lua side.
func (r *redisInventoryRepository) DeductInventoryNoStream(
	ctx context.Context,
	ticketTypeID uuid.UUID,
	count int,
) (domain.DeductInventoryResult, error) {
	keys := []string{inventoryKey(ticketTypeID), ticketTypeMetaKey(ticketTypeID)}

	script, ok := r.scripts["deduct_no_xadd"]
	if !ok {
		return domain.DeductInventoryResult{}, errDeductScriptNotFound
	}

	raw, err := script.Run(ctx, r.client, keys, count).Result()
	if err != nil {
		return domain.DeductInventoryResult{}, err
	}

	res, ok := raw.([]interface{})
	if !ok || len(res) == 0 {
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua result %T: %w", raw, errUnexpectedLuaResult)
	}

	status, ok := res[0].(string)
	if !ok {
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua status %T: %w", res[0], errUnexpectedLuaResult)
	}

	switch status {
	case "ok":
		if len(res) != 4 {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected ok payload len=%d: %w", len(res), errUnexpectedLuaResult)
		}
		eventIDStr, ok1 := res[1].(string)
		amountCentsStr, ok2 := res[2].(string)
		currency, ok3 := res[3].(string)
		if !ok1 || !ok2 || !ok3 {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: malformed ok payload: %w", errUnexpectedLuaResult)
		}
		eventID, parseEventErr := uuid.Parse(eventIDStr)
		if parseEventErr != nil {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: parse event_id %q: %w", eventIDStr, parseEventErr)
		}
		amountCents, parseAmountErr := strconv.ParseInt(amountCentsStr, 10, 64)
		if parseAmountErr != nil {
			return domain.DeductInventoryResult{}, fmt.Errorf("redis: parse amount_cents %q: %w", amountCentsStr, parseAmountErr)
		}
		return domain.DeductInventoryResult{
			Accepted:    true,
			EventID:     eventID,
			AmountCents: amountCents,
			Currency:    currency,
		}, nil
	case "sold_out":
		return domain.DeductInventoryResult{Accepted: false}, nil
	case "metadata_missing":
		return domain.DeductInventoryResult{}, domain.ErrTicketTypeRuntimeMetadataMissing
	default:
		return domain.DeductInventoryResult{}, fmt.Errorf("redis: unexpected lua status %q: %w", status, errUnexpectedLuaResult)
	}
}

func (r *redisInventoryRepository) RevertInventory(ctx context.Context, ticketTypeID uuid.UUID, count int, compensationID string) error {
	keys := []string{inventoryKey(ticketTypeID), "saga:reverted:" + compensationID}

	// Dedicated 1-element pool — see revertArgsPool comment for why
	// it's split from the deduct path.
	argsPtr := revertArgsPool.Get().(*[]interface{})
	args := *argsPtr
	args[0] = count
	defer revertArgsPool.Put(argsPtr)

	script, ok := r.scripts["revert"]
	if !ok {
		return errRevertScriptNotFound
	}

	return script.Run(ctx, r.client, keys, args...).Err()
}
