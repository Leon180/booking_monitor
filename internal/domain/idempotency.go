package domain

import "context"

// IdempotencyResult stores the cached outcome of a processed request.
//
// No json/db tags — domain types stay JSON-unaware (per
// `.claude/rules/golang/coding-style.md` rule 7). The Redis
// implementation marshals via its own `idempotencyRecord` translator
// (`internal/infrastructure/cache/idempotency.go`) so the wire format
// is owned by the boundary, not the domain.
type IdempotencyResult struct {
	StatusCode int
	Body       string
}

// IdempotencyRepository provides idempotency key storage.
// Implementations should store results with a TTL to prevent unbounded growth.
type IdempotencyRepository interface {
	Get(ctx context.Context, key string) (*IdempotencyResult, error)
	Set(ctx context.Context, key string, result *IdempotencyResult) error
}
