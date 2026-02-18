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
	fx.Provide(NewRedisOrderQueue),
)

type redisInventoryRepository struct {
	client  *redis.Client
	scripts map[string]*redis.Script
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

	// Scripts are loaded lazily by redis.Script
	log.Infow("Connected to Redis successfully", "addr", redisCfg.Addr, "pool_size", redisCfg.PoolSize)
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
	args := []interface{}{userID, count, eventID}

	// Get script
	script, ok := r.scripts["deduct"]
	if !ok {
		return false, fmt.Errorf("script 'deduct' not found")
	}

	// EXEC LUA using redis.Script (handles LOAD if missing)
	res, err := script.Run(ctx, r.client, keys, args...).Int()

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

func (r *redisInventoryRepository) RevertInventory(ctx context.Context, eventID int, userID int, count int) error {
	keys := []string{inventoryKey(eventID), buyersKey(eventID)}
	args := []interface{}{userID, count}

	script, ok := r.scripts["revert"]
	if !ok {
		return fmt.Errorf("script 'revert' not found")
	}

	return script.Run(ctx, r.client, keys, args...).Err()
}
