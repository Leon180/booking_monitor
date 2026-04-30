package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
)

// TestRedisIdempotency_GetSetRoundtrip pins the wire-format contract
// between domain.IdempotencyResult and the cache-side idempotencyRecord
// translator. PR #37 moved the `json:` tags out of the domain type and
// into idempotencyRecord; N4 added the `fingerprint` wire field. This
// test catches any future field-rename drift (e.g. someone renames
// `status_code` → `code` or `fingerprint` → `body_hash` in the wire
// record without updating wire-compatible cached values).
//
// Without this test, a translator-side typo would only surface in
// production when stored cached entries fail to unmarshal — long
// after the offending PR has merged.
func TestRedisIdempotency_GetSetRoundtrip(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	repo := NewRedisIdempotencyRepository(rdb, &config.Config{
		Redis: config.RedisConfig{IdempotencyTTL: time.Hour},
	})

	ctx := context.Background()
	key := "user-1:order-abc"
	wantFP := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9" // sha256 of "hello world"

	// Cache miss → (nil, "", nil) — verify before populating
	got, gotFP, err := repo.Get(ctx, key)
	require.NoError(t, err)
	assert.Nil(t, got, "cache miss must return (nil, ...) — not an error, not a zero-value struct")
	assert.Empty(t, gotFP, "cache miss must return empty fingerprint")

	// Set with fingerprint
	want := &domain.IdempotencyResult{StatusCode: 202, Body: `{"message":"booking accepted"}`}
	require.NoError(t, repo.Set(ctx, key, want, wantFP))

	// Get back — all three fields must survive the marshal+unmarshal round trip
	got, gotFP, err = repo.Get(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want.StatusCode, got.StatusCode, "status_code must survive roundtrip")
	assert.Equal(t, want.Body, got.Body, "body must survive roundtrip")
	assert.Equal(t, wantFP, gotFP, "fingerprint must survive roundtrip")

	// Verify the stored wire format (catches drift in idempotencyRecord
	// json tags — if someone changes `json:"status_code"` to
	// `json:"code"`, this assertion fails before the rename ships).
	raw, err := rdb.Get(ctx, idempotencyKey(key)).Result()
	require.NoError(t, err)
	wantWire := `{"status_code":202,"body":"{\"message\":\"booking accepted\"}","fingerprint":"` + wantFP + `"}`
	assert.JSONEq(t, wantWire, raw,
		"wire format must remain {status_code, body, fingerprint} — domain field names are NOT the wire contract")
}

// TestRedisIdempotency_TTL pins that Set respects the configured TTL.
// Catches any regression where a TTL=0 (no expiry) or a hardcoded TTL
// silently replaces the configured value.
func TestRedisIdempotency_TTL(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	repo := NewRedisIdempotencyRepository(rdb, &config.Config{
		Redis: config.RedisConfig{IdempotencyTTL: 5 * time.Second},
	})

	ctx := context.Background()
	key := "ttl-probe"

	require.NoError(t, repo.Set(ctx, key, &domain.IdempotencyResult{StatusCode: 200, Body: "ok"}, "fp"))

	// miniredis tracks TTLs; verify the TTL is exactly what we configured.
	ttl := s.TTL(idempotencyKey(key))
	assert.Equal(t, 5*time.Second, ttl, "Set must apply cfg.Redis.IdempotencyTTL")
}

// TestRedisIdempotency_LegacyEntryReadback verifies the lazy-migration
// contract: a cache entry stored under the pre-N4 wire format (no
// `fingerprint` field) unmarshals successfully, the result is non-nil,
// and the returned fingerprint is empty — which the handler interprets
// as "match, replay, lazy-write-back". Without this test, an `omitempty`
// regression on the wire field could fail-closed against legacy entries
// at deploy time, breaking idempotency for every cached pre-N4 client.
func TestRedisIdempotency_LegacyEntryReadback(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	repo := NewRedisIdempotencyRepository(rdb, &config.Config{
		Redis: config.RedisConfig{IdempotencyTTL: time.Hour},
	})

	ctx := context.Background()
	key := "legacy-key"

	// Hand-write the pre-N4 wire format (no `fingerprint` field) — this
	// is exactly what production Redis contains for any entry written
	// before N4 rolled out.
	legacy := `{"status_code":200,"body":"{\"message\":\"booking successful\"}"}`
	require.NoError(t, rdb.Set(ctx, idempotencyKey(key), legacy, time.Hour).Err())

	got, gotFP, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, got, "legacy entry must read back as a non-nil hit")
	assert.Equal(t, 200, got.StatusCode)
	assert.Equal(t, `{"message":"booking successful"}`, got.Body)
	assert.Empty(t, gotFP,
		"legacy entry must report empty fingerprint — handler signal for lazy-migration write-back")
}

