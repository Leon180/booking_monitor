package cache

import (
	"context"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/observability"
)

// cacheLabelIdempotency is the `cache` label value reported on the
// hit/miss counters. Lifted to a const so the two Inc sites can't
// drift, and kept HERE (decorator file) rather than in the underlying
// repo so the storage layer stays observability-unaware.
const cacheLabelIdempotency = "idempotency"

// instrumentedIdempotencyRepository decorates a plain
// domain.IdempotencyRepository with hit/miss Prometheus counters.
//
// Why a decorator (not inline Inc calls in the storage repo):
//   - Storage code stays purely about Redis I/O — no dependency on
//     observability, no global-counter side effects in repo tests.
//   - Mirrors the established TracingBookingHandler pattern in
//     internal/infrastructure/api/ — single source of truth for
//     "how this codebase wraps cross-cutting concerns".
//   - Composable: future tracing / retry / circuit-breaker decorators
//     stack via additional fx.Decorate calls without touching this
//     file or the Redis repo.
//
// Wired via fx.Decorate in cache/redis.go::Module so consumers ask
// for domain.IdempotencyRepository and transparently get the
// instrumented one — no opt-in burden at call sites.
type instrumentedIdempotencyRepository struct {
	inner domain.IdempotencyRepository
}

// NewInstrumentedIdempotencyRepository returns the decorator. Accepts
// + returns the same interface so fx.Decorate transparently swaps it
// in over the plain repo provided by NewRedisIdempotencyRepository.
func NewInstrumentedIdempotencyRepository(inner domain.IdempotencyRepository) domain.IdempotencyRepository {
	return &instrumentedIdempotencyRepository{inner: inner}
}

// Get records hit/miss based on the inner repo's return value:
//
//	(result, nil) → hit   — found a cached entry
//	(nil, nil)    → miss  — lookup succeeded, no entry
//	(nil, err)    → NEITHER — infra failure (Redis down, unmarshal
//	                          fail). Counting these as "miss" would
//	                          inflate the miss rate during outages
//	                          and drown the real signal; counting
//	                          them as "hit" is obviously wrong. The
//	                          right surface for infra failures is
//	                          a separate error counter, scheduled
//	                          for N3 (per-alert runbooks + SLO).
func (i *instrumentedIdempotencyRepository) Get(ctx context.Context, key string) (*domain.IdempotencyResult, error) {
	res, err := i.inner.Get(ctx, key)
	if err != nil {
		return res, err
	}
	if res == nil {
		observability.CacheMissesTotal.WithLabelValues(cacheLabelIdempotency).Inc()
	} else {
		observability.CacheHitsTotal.WithLabelValues(cacheLabelIdempotency).Inc()
	}
	return res, err
}

// Set is a transparent pass-through. There's no hit/miss to record
// on a write, and no separate "set succeeded" counter today; if one
// is added later it lives here, not in the storage repo.
func (i *instrumentedIdempotencyRepository) Set(ctx context.Context, key string, result *domain.IdempotencyResult) error {
	return i.inner.Set(ctx, key, result)
}
