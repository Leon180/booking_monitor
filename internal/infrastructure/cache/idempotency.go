package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"

	"github.com/redis/go-redis/v9"
)

type redisIdempotencyRepository struct {
	client         *redis.Client
	idempotencyTTL time.Duration
}

func NewRedisIdempotencyRepository(client *redis.Client, cfg *config.Config) domain.IdempotencyRepository {
	return &redisIdempotencyRepository{
		client:         client,
		idempotencyTTL: cfg.Redis.IdempotencyTTL,
	}
}

func idempotencyKey(key string) string {
	return "idempotency:" + key
}

// idempotencyRecord is the Redis-side wire format for a cached
// idempotency result. The `json:` tags live HERE, not on
// `domain.IdempotencyResult`, so the domain type stays JSON-unaware
// (boundary owns its serialisation, per coding-style rule 7).
//
// Translation between the two is one-line each way; the indirection
// pays back the moment we ever change wire keys (`status_code` →
// `statusCode`) without touching the domain, or migrate to a
// different serialiser (msgpack, protobuf) without touching the
// domain.
type idempotencyRecord struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

// Get returns the cached entry, (nil, nil) on cache miss
// (`redis.Nil`), or a wrapped error on infrastructure failure. Hit /
// miss counting is the decorator's job (instrumented_idempotency.go);
// keeping it out of here means storage tests don't mutate the global
// Prometheus registry as a side effect.
func (r *redisIdempotencyRepository) Get(ctx context.Context, key string) (*domain.IdempotencyResult, error) {
	val, err := r.client.Get(ctx, idempotencyKey(key)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil // Cache miss — not found
		}
		return nil, fmt.Errorf("idempotency Get: redis: %w", err)
	}

	var rec idempotencyRecord
	if err := json.Unmarshal([]byte(val), &rec); err != nil {
		return nil, fmt.Errorf("idempotency Get: unmarshal: %w", err)
	}
	return &domain.IdempotencyResult{
		StatusCode: rec.StatusCode,
		Body:       rec.Body,
	}, nil
}

func (r *redisIdempotencyRepository) Set(ctx context.Context, key string, result *domain.IdempotencyResult) error {
	rec := idempotencyRecord{
		StatusCode: result.StatusCode,
		Body:       result.Body,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("idempotency Set: marshal: %w", err)
	}
	if err := r.client.Set(ctx, idempotencyKey(key), data, r.idempotencyTTL).Err(); err != nil {
		return fmt.Errorf("idempotency Set: redis: %w", err)
	}
	return nil
}
