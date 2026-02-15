package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"booking_monitor/internal/domain"
)

// Module provides the Redis client and InventoryRepository.
var Module = fx.Options(
	fx.Provide(NewRedisClient),
	fx.Provide(NewRedisInventoryRepository),
)

type redisInventoryRepository struct {
	client *redis.Client
}

func NewRedisClient(log *zap.SugaredLogger) *redis.Client {
	// Redis Configuration for High Concurrency
	// ReadTimeout/WriteTimeout: 2s (Fail fast but allow some buffer for network/load)
	// PoolSize: 100 (Handle many concurrent goroutines)
	// MinIdleConns: 10 (Keep warm connections)
	opts := &redis.Options{
		Addr:         "redis:6379", // Service name in docker-compose
		Password:     "",           // No password set
		DB:           0,            // Default DB
		PoolSize:     100,
		MinIdleConns: 10,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolTimeout:  2200 * time.Millisecond, // Slightly longer than RW timeout
	}

	client := redis.NewClient(opts)

	// Verify connection on startup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		log.Fatalw("Failed to connect to Redis", "error", err)
	}

	log.Info("Connected to Redis successfully")
	return client
}

func NewRedisInventoryRepository(client *redis.Client) domain.InventoryRepository {
	return &redisInventoryRepository{client: client}
}

// key helper: event:{id}:qty
func inventoryKey(eventID int) string {
	return fmt.Sprintf("event:%d:qty", eventID)
}

func (r *redisInventoryRepository) SetInventory(ctx context.Context, eventID int, count int) error {
	return r.client.Set(ctx, inventoryKey(eventID), count, 0).Err()
}

func (r *redisInventoryRepository) DeductInventory(ctx context.Context, eventID int, count int) (bool, error) {
	key := inventoryKey(eventID)

	// Atomic DECRBY
	newVal, err := r.client.DecrBy(ctx, key, int64(count)).Result()
	if err != nil {
		return false, err
	}

	// Check if we oversold
	if newVal < 0 {
		// Rollback (INCRBY)
		// Note: There is a race condition here technically (someone else could see negative),
		// but since we return failure, the inventory remains consistent eventually.
		// Detailed LUA script prevents seeing negative, but for Phase 2 basic implementation:
		r.client.IncrBy(ctx, key, int64(count))
		return false, nil // Sold Out
	}

	return true, nil
}
