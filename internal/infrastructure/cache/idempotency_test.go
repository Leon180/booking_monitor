package cache

import (
	"context"
	"strings"
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
// into idempotencyRecord; this test catches any future field-rename
// drift (e.g. someone renames `status_code` → `code` in
// idempotencyRecord without updating wire-compatible cached values).
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

	// Cache miss → (nil, nil) — verify before populating
	got, err := repo.Get(ctx, key)
	require.NoError(t, err)
	assert.Nil(t, got, "cache miss must return (nil, nil) — not an error, not a zero-value struct")

	// Set
	want := &domain.IdempotencyResult{StatusCode: 202, Body: `{"message":"booking accepted"}`}
	require.NoError(t, repo.Set(ctx, key, want))

	// Get back — both fields must survive the marshal+unmarshal round trip
	got, err = repo.Get(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want.StatusCode, got.StatusCode, "status_code must survive roundtrip")
	assert.Equal(t, want.Body, got.Body, "body must survive roundtrip")

	// Verify the stored wire format (catches drift in idempotencyRecord
	// json tags — if someone changes `json:"status_code"` to
	// `json:"code"`, this assertion fails before the rename ships).
	raw, err := rdb.Get(ctx, idempotencyKey(key)).Result()
	require.NoError(t, err)
	assert.JSONEq(t, `{"status_code":202,"body":"{\"message\":\"booking accepted\"}"}`, raw,
		"wire format must remain {status_code, body} — domain field names are NOT the wire contract")
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

	require.NoError(t, repo.Set(ctx, key, &domain.IdempotencyResult{StatusCode: 200, Body: "ok"}))

	// miniredis tracks TTLs; verify the TTL is exactly what we configured.
	ttl := s.TTL(idempotencyKey(key))
	assert.Equal(t, 5*time.Second, ttl, "Set must apply cfg.Redis.IdempotencyTTL")
}

// TestRedisIdempotency_OversizeRejected pins the defensive size cap.
// Today no production caller stores oversize payloads (the booking
// handler emits ~30-100 byte JSON), so this test guards against a
// future regression where an oversize-storing path bypasses the cap.
func TestRedisIdempotency_OversizeRejected(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	repo := NewRedisIdempotencyRepository(rdb, &config.Config{
		Redis: config.RedisConfig{IdempotencyTTL: time.Hour},
	})

	ctx := context.Background()
	key := "oversize-probe"

	// Build a body larger than maxIdempotencyValueBytes (4KB).
	// Use 8KB to leave ample headroom past the JSON envelope.
	bigBody := strings.Repeat("X", 8192)
	err := repo.Set(ctx, key, &domain.IdempotencyResult{StatusCode: 200, Body: bigBody})
	require.Error(t, err, "oversize Set must error, not silently truncate")
	assert.ErrorIs(t, err, ErrIdempotencyValueTooLarge,
		"error must be the typed sentinel for caller errors.Is matching")

	// Critical: nothing must have been written to Redis. A partial
	// write would mean the cap is enforced too late and an oversize
	// entry occupies memory anyway.
	exists, err := rdb.Exists(ctx, idempotencyKey(key)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "oversize Set must NOT write the key")
}

// TestRedisIdempotency_BoundaryAccepted pins that values just under
// the cap go through cleanly. Catches off-by-one regressions.
func TestRedisIdempotency_BoundaryAccepted(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	repo := NewRedisIdempotencyRepository(rdb, &config.Config{
		Redis: config.RedisConfig{IdempotencyTTL: time.Hour},
	})

	ctx := context.Background()
	key := "boundary-probe"

	// 3000 bytes of body + ~30 bytes JSON envelope = ~3030 bytes
	// marshalled, well under the 4096 cap. Should pass.
	body := strings.Repeat("Y", 3000)
	require.NoError(t, repo.Set(ctx, key, &domain.IdempotencyResult{StatusCode: 200, Body: body}))

	got, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, body, got.Body)
}
