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
//
// Fingerprint (added in N4) carries the hex SHA-256 of the request
// body that produced this cached response — enables Stripe-style
// same-key/different-body 409 detection. `omitempty` on the JSON tag
// preserves backward-compat with pre-N4 entries: legacy values that
// have no `fingerprint` field unmarshal to an empty string, which the
// handler treats as "match, replay" + lazy write-back. Without
// `omitempty`, every old entry would round-trip as `"fingerprint":""`
// and we'd lose the ability to distinguish "explicitly empty" from
// "absent" — relevant if a future feature treats them differently.
type idempotencyRecord struct {
	StatusCode  int    `json:"status_code"`
	Body        string `json:"body"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Get returns (result, fingerprint, error). On cache miss — `redis.Nil`
// from GET — returns (nil, "", nil). On infrastructure failure
// returns a wrapped error. Hit / miss counting is the decorator's job
// (instrumented_idempotency.go); keeping it out of here means storage
// tests don't mutate the global Prometheus registry as a side effect.
//
// An empty fingerprint with a non-nil result is the LEGACY-ENTRY
// signal: the cache record predates N4 (pre-fingerprint, pre-2026-04-30
// roughly) and was stored without the `fingerprint` field. The handler
// treats this as "replay and write back" — see handler.go for the
// migration logic.
func (r *redisIdempotencyRepository) Get(ctx context.Context, key string) (*domain.IdempotencyResult, string, error) {
	val, err := r.client.Get(ctx, idempotencyKey(key)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, "", nil // Cache miss — not found
		}
		return nil, "", fmt.Errorf("idempotency Get: redis: %w", err)
	}

	var rec idempotencyRecord
	if err := json.Unmarshal([]byte(val), &rec); err != nil {
		return nil, "", fmt.Errorf("idempotency Get: unmarshal: %w", err)
	}
	return &domain.IdempotencyResult{
		StatusCode: rec.StatusCode,
		Body:       rec.Body,
	}, rec.Fingerprint, nil
}

func (r *redisIdempotencyRepository) Set(ctx context.Context, key string, result *domain.IdempotencyResult, fingerprint string) error {
	rec := idempotencyRecord{
		StatusCode:  result.StatusCode,
		Body:        result.Body,
		Fingerprint: fingerprint,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("idempotency Set: marshal: %w", err)
	}
	// Size validation lives at the HTTP boundary
	// (api/middleware/body_size.go) NOT here, per industry convention
	// (Stripe / Shopify / GitHub Octokit). The cache layer trusts
	// pre-validated input. See PROJECT_SPEC §6.8 for the layering
	// rationale.
	if err := r.client.Set(ctx, idempotencyKey(key), data, r.idempotencyTTL).Err(); err != nil {
		return fmt.Errorf("idempotency Set: redis: %w", err)
	}
	return nil
}
