package cache

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/observability"
)

// fakeIdempotencyRepo is a deterministic in-memory IdempotencyRepository
// used to drive the decorator under each return shape:
//
//	(result, nil) — hit
//	(nil, nil)    — miss
//	(nil, err)    — infrastructure failure
//
// Avoids miniredis so the test runs in microseconds and never touches
// network.
type fakeIdempotencyRepo struct {
	getResult *domain.IdempotencyResult
	getFP     string
	getErr    error
	setErr    error
	setCalls  int
}

func (f *fakeIdempotencyRepo) Get(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
	return f.getResult, f.getFP, f.getErr
}

func (f *fakeIdempotencyRepo) Set(_ context.Context, _ string, _ *domain.IdempotencyResult, _ string) error {
	f.setCalls++
	return f.setErr
}

func TestInstrumentedIdempotency_Get_Hit_IncrementsHitCounter(t *testing.T) {
	hitsBefore := testutil.ToFloat64(observability.CacheHitsTotal.WithLabelValues(cacheLabelIdempotency))
	missesBefore := testutil.ToFloat64(observability.CacheMissesTotal.WithLabelValues(cacheLabelIdempotency))

	repo := NewInstrumentedIdempotencyRepository(&fakeIdempotencyRepo{
		getResult: &domain.IdempotencyResult{StatusCode: 200, Body: "ok"},
	})

	got, _, err := repo.Get(context.Background(), "k")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 200, got.StatusCode)

	assert.Equal(t, hitsBefore+1, testutil.ToFloat64(observability.CacheHitsTotal.WithLabelValues(cacheLabelIdempotency)),
		"hit must increment cache_hits_total")
	assert.Equal(t, missesBefore, testutil.ToFloat64(observability.CacheMissesTotal.WithLabelValues(cacheLabelIdempotency)),
		"hit must NOT touch cache_misses_total")
}

func TestInstrumentedIdempotency_Get_Miss_IncrementsMissCounter(t *testing.T) {
	hitsBefore := testutil.ToFloat64(observability.CacheHitsTotal.WithLabelValues(cacheLabelIdempotency))
	missesBefore := testutil.ToFloat64(observability.CacheMissesTotal.WithLabelValues(cacheLabelIdempotency))

	repo := NewInstrumentedIdempotencyRepository(&fakeIdempotencyRepo{
		getResult: nil,
		getErr:    nil,
	})

	got, _, err := repo.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.Nil(t, got)

	assert.Equal(t, missesBefore+1, testutil.ToFloat64(observability.CacheMissesTotal.WithLabelValues(cacheLabelIdempotency)),
		"miss must increment cache_misses_total")
	assert.Equal(t, hitsBefore, testutil.ToFloat64(observability.CacheHitsTotal.WithLabelValues(cacheLabelIdempotency)),
		"miss must NOT touch cache_hits_total")
}

func TestInstrumentedIdempotency_Get_InfraError_IncrementsErrorCounter(t *testing.T) {
	hitsBefore := testutil.ToFloat64(observability.CacheHitsTotal.WithLabelValues(cacheLabelIdempotency))
	missesBefore := testutil.ToFloat64(observability.CacheMissesTotal.WithLabelValues(cacheLabelIdempotency))
	errorsBefore := testutil.ToFloat64(observability.IdempotencyCacheGetErrorsTotal)

	repo := NewInstrumentedIdempotencyRepository(&fakeIdempotencyRepo{
		getErr: errors.New("redis: connection refused"),
	})

	got, _, err := repo.Get(context.Background(), "k")
	require.Error(t, err, "infra error must propagate")
	assert.Nil(t, got)

	// Critical: an outage that errors every Get must NOT inflate the
	// miss rate. This is the whole reason the decorator gates on
	// (err == nil) before touching hit/miss.
	assert.Equal(t, hitsBefore, testutil.ToFloat64(observability.CacheHitsTotal.WithLabelValues(cacheLabelIdempotency)),
		"infra error must NOT increment cache_hits_total")
	assert.Equal(t, missesBefore, testutil.ToFloat64(observability.CacheMissesTotal.WithLabelValues(cacheLabelIdempotency)),
		"infra error must NOT increment cache_misses_total")

	// But it MUST increment the dedicated error counter — without
	// this, a Redis outage looks like "traffic dropped" on the cache
	// dashboard. The companion alert in monitoring.md depends on
	// this signal being present.
	assert.Equal(t, errorsBefore+1, testutil.ToFloat64(observability.IdempotencyCacheGetErrorsTotal),
		"infra error MUST increment idempotency_cache_get_errors_total — operator's only signal that idempotency protection is suspended")
}

func TestInstrumentedIdempotency_Set_PassesThrough(t *testing.T) {
	inner := &fakeIdempotencyRepo{}
	repo := NewInstrumentedIdempotencyRepository(inner)

	err := repo.Set(context.Background(), "k", &domain.IdempotencyResult{StatusCode: 201, Body: "x"}, "fp")
	require.NoError(t, err)
	assert.Equal(t, 1, inner.setCalls, "Set must reach the inner repo")
}

func TestInstrumentedIdempotency_Set_PropagatesError(t *testing.T) {
	want := errors.New("redis: write failed")
	inner := &fakeIdempotencyRepo{setErr: want}
	repo := NewInstrumentedIdempotencyRepository(inner)

	err := repo.Set(context.Background(), "k", &domain.IdempotencyResult{StatusCode: 500, Body: ""}, "fp")
	require.ErrorIs(t, err, want, "Set must propagate inner errors verbatim")
}
