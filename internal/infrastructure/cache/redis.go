package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	_ "embed"
)

// Module provides the Redis client and InventoryRepository.
var Module = fx.Options(
	fx.Provide(NewRedisClient),
	fx.Provide(NewRedisInventoryRepository),
)

type redisInventoryRepository struct {
	client *redis.Client
}

func NewRedisClient(cfg *config.Config, log *zap.SugaredLogger) *redis.Client {
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
		log.Fatalw("Failed to connect to Redis", "error", err)
	}

	// Load scripts
	if err := loadLuaScript(client); err != nil {
		log.Errorw("Failed to load Lua script", "error", err)
	} else {
		log.Infow("Lua script loaded successfully")
	}

	log.Infow("Connected to Redis successfully", "addr", redisCfg.Addr, "pool_size", redisCfg.PoolSize)
	return client
}

//go:embed lua/deduct.lua
var deductScript string

var deductSha string

func loadLuaScript(client *redis.Client) error {
	var err error
	deductSha, err = client.ScriptLoad(context.Background(), deductScript).Result()
	return err
}

func NewRedisInventoryRepository(client *redis.Client) domain.InventoryRepository {
	// Ensure script is loaded if not already (simple singleton-ish check for this demo)
	if deductSha == "" {
		_ = loadLuaScript(client)
	}
	return &redisInventoryRepository{client: client}
}

// key helper: event:{id}:qty
func inventoryKey(eventID int) string {
	return fmt.Sprintf("event:%d:qty", eventID)
}

// key helper: event:{id}:buyers
func buyersKey(eventID int) string {
	return fmt.Sprintf("event:%d:buyers", eventID)
}

func (r *redisInventoryRepository) SetInventory(ctx context.Context, eventID int, count int) error {
	return r.client.Set(ctx, inventoryKey(eventID), count, 0).Err()
}

func (r *redisInventoryRepository) DeductInventory(ctx context.Context, eventID int, userID int, count int) (bool, error) {
	keys := []string{inventoryKey(eventID), buyersKey(eventID)}
	args := []interface{}{userID, count}

	// EXEC LUA
	res, err := r.client.EvalSha(ctx, deductSha, keys, args...).Int()

	// Handle Script Missing (Redis restart? Re-load)
	if err != nil && isScriptMissing(err) {
		if loadErr := loadLuaScript(r.client); loadErr != nil {
			return false, fmt.Errorf("reload script failed: %w", loadErr)
		}
		// Retry once
		res, err = r.client.EvalSha(ctx, deductSha, keys, args...).Int()
	}

	if err != nil {
		return false, err
	}

	switch res {
	case 1:
		return true, nil
	case -1:
		return false, nil // Sold Out
	case -2:
		return false, domain.ErrUserAlreadyBought
	default:
		return false, fmt.Errorf("unexpected lua result: %d", res)
	}
}

func isScriptMissing(err error) bool {
	return err.Error() == "NOSCRIPT No matching script. Please use EVAL."
}
