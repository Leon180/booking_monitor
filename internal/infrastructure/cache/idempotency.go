package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"

	"github.com/redis/go-redis/v9"
)

// maxIdempotencyValueBytes caps the marshalled wire-format size of a
// cached idempotency entry. Today the only producer is the booking
// handler emitting fixed-shape JSON responses (~30-100 bytes), so
// the cap is purely defensive — it prevents a future code path that
// stored arbitrary client data through this repo from creating a
// memory-amplification vector. Stripe documents a similar 1KB cap
// on stored responses; we use 4KB to be generous for any realistic
// JSON response we might emit in the future.
//
// Set returns ErrIdempotencyValueTooLarge on overflow; the handler
// already discards Set errors (response was already sent — losing
// the cache entry just means the next retry re-processes), so the
// cap is enforced loudly via metric + log without affecting the
// in-flight request.
const maxIdempotencyValueBytes = 4096

// ErrIdempotencyValueTooLarge fires when a marshalled idempotency
// record exceeds maxIdempotencyValueBytes. Sentinel so callers can
// errors.Is without string-matching.
var ErrIdempotencyValueTooLarge = errors.New("idempotency value exceeds size cap")

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
	// Defensive size cap. See `maxIdempotencyValueBytes` doc for the
	// rationale; today the booking handler stores ~30-100 byte JSON
	// responses, so this never fires under normal traffic. If it
	// ever does, the metric (cache_idempotency_oversize_total) and
	// log surface the offending key for debugging.
	if len(data) > maxIdempotencyValueBytes {
		observability.CacheIdempotencyOversizeTotal.Inc()
		return fmt.Errorf("idempotency Set: payload %d bytes (cap %d): %w",
			len(data), maxIdempotencyValueBytes, ErrIdempotencyValueTooLarge)
	}
	if err := r.client.Set(ctx, idempotencyKey(key), data, r.idempotencyTTL).Err(); err != nil {
		return fmt.Errorf("idempotency Set: redis: %w", err)
	}
	return nil
}
