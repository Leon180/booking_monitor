package domain

import "context"

// IdempotencyResult stores the cached outcome of a processed request.
type IdempotencyResult struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

// IdempotencyRepository provides idempotency key storage.
// Implementations should store results with a TTL to prevent unbounded growth.
type IdempotencyRepository interface {
	Get(ctx context.Context, key string) (*IdempotencyResult, error)
	Set(ctx context.Context, key string, result *IdempotencyResult) error
}
