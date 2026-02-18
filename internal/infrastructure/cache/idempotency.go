package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"booking_monitor/internal/domain"

	"github.com/redis/go-redis/v9"
)

const idempotencyTTL = 24 * time.Hour

type redisIdempotencyRepository struct {
	client *redis.Client
}

func NewRedisIdempotencyRepository(client *redis.Client) domain.IdempotencyRepository {
	return &redisIdempotencyRepository{client: client}
}

func idempotencyKey(key string) string {
	return "idempotency:" + key
}

func (r *redisIdempotencyRepository) Get(ctx context.Context, key string) (*domain.IdempotencyResult, error) {
	val, err := r.client.Get(ctx, idempotencyKey(key)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil // Cache miss — not found
		}
		return nil, err
	}

	var result domain.IdempotencyResult
	if err := json.Unmarshal([]byte(val), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *redisIdempotencyRepository) Set(ctx context.Context, key string, result *domain.IdempotencyResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, idempotencyKey(key), data, idempotencyTTL).Err()
}
