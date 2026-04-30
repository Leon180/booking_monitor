package middleware_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/middleware"
)

func init() { gin.SetMode(gin.TestMode) }

// stubIdempotencyRepo lets each test control whether the in-memory
// cache hits or misses. The Set side records last-write so tests can
// verify that the middleware caches the new response shape.
type stubIdempotencyRepo struct {
	getFn   func(ctx context.Context, key string) (*domain.IdempotencyResult, string, error)
	setFn   func(ctx context.Context, key string, result *domain.IdempotencyResult, fingerprint string) error
	lastIn  *domain.IdempotencyResult
	lastFP  string
	lastKey string
}

func (s *stubIdempotencyRepo) Get(ctx context.Context, key string) (*domain.IdempotencyResult, string, error) {
	if s.getFn == nil {
		return nil, "", nil
	}
	return s.getFn(ctx, key)
}

func (s *stubIdempotencyRepo) Set(ctx context.Context, key string, r *domain.IdempotencyResult, fp string) error {
	s.lastIn = r
	s.lastFP = fp
	s.lastKey = key
	if s.setFn == nil {
		return nil
	}
	return s.setFn(ctx, key, r, fp)
}

// fingerprintFor mirrors the middleware's hex-SHA256-of-raw-bytes
// fingerprinting so tests can pre-compute the value the middleware
// would store. Centralised so a future hash algorithm change ripples
// through this single helper rather than every test fixture.
func fingerprintFor(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// newRouter wires the middleware against a single trivial inner
// handler that echoes the body and writes the desired status. Each
// test customises the inner handler when it needs non-default
// behaviour (e.g., 5xx, no-write).
func newRouter(idem domain.IdempotencyRepository, inner gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.POST("/book", middleware.Idempotency(idem), inner)
	return r
}

// successHandler is the canonical 202-Accepted handler used by most
// match/miss tests. Mirrors the production booking response shape
// closely enough to test the cache-write contract.
func successHandler(c *gin.Context) {
	c.Data(http.StatusAccepted, "application/json", []byte(`{"order_id":"abc","status":"processing"}`))
}

// TestIdempotency_PassThrough_NoKey: no Idempotency-Key header, no
// caching path engaged at all. Inner handler runs, response is
// returned, repo is NEVER touched.
func TestIdempotency_PassThrough_NoKey(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			t.Fatal("Get must NOT be called when Idempotency-Key is absent")
			return nil, "", nil
		},
	}
	r := newRouter(idem, successHandler)

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Nil(t, idem.lastIn, "Set must NOT be called when key is absent")
}

// TestIdempotency_Match: same key + same body → replay cached
// response with X-Idempotency-Replayed header, inner handler is
// NOT invoked.
func TestIdempotency_Match(t *testing.T) {
	t.Parallel()

	cachedBody := `{"order_id":"cached-order","status":"processing"}`
	requestBody := `{"x":1}`
	matchingFP := fingerprintFor(requestBody)

	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, key string) (*domain.IdempotencyResult, string, error) {
			assert.Equal(t, "k1", key)
			return &domain.IdempotencyResult{StatusCode: http.StatusAccepted, Body: cachedBody}, matchingFP, nil
		},
	}
	innerCalled := false
	r := newRouter(idem, func(c *gin.Context) {
		innerCalled = true
		c.Data(http.StatusAccepted, "application/json", []byte(`{"different":"body"}`))
	})

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.False(t, innerCalled, "inner handler MUST NOT be called on cache hit + match")
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"))
	assert.JSONEq(t, cachedBody, w.Body.String())
}

// TestIdempotency_Mismatch: same key + different body → 409 Conflict.
// Cached body is NOT returned (would mislead the client).
func TestIdempotency_Mismatch(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			return &domain.IdempotencyResult{
				StatusCode: http.StatusAccepted,
				Body:       `{"order_id":"original"}`,
			}, fingerprintFor(`{"original":"body"}`), nil
		},
	}
	innerCalled := false
	r := newRouter(idem, func(c *gin.Context) {
		innerCalled = true
	})

	// Different body from the one whose fingerprint is cached
	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"new":"body"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.False(t, innerCalled, "inner handler MUST NOT be called on mismatch")
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Empty(t, w.Header().Get("X-Idempotency-Replayed"),
		"mismatch is NOT a replay; the header must NOT be set")
	assert.Contains(t, w.Body.String(), "Idempotency-Key reused with a different request body")
}

// TestIdempotency_LegacyMatch_ReplaysAndWritesBack: cached entry has
// no fingerprint (pre-N4 entry). Middleware MUST replay AND write
// back the freshly-computed fingerprint so subsequent replays
// validate.
func TestIdempotency_LegacyMatch_ReplaysAndWritesBack(t *testing.T) {
	t.Parallel()

	cachedBody := `{"order_id":"legacy"}`
	requestBody := `{"x":1}`
	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			return &domain.IdempotencyResult{StatusCode: http.StatusAccepted, Body: cachedBody}, "", nil
		},
	}
	r := newRouter(idem, func(c *gin.Context) {
		t.Fatal("legacy replay must NOT call inner handler")
	})

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "legacy-k")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"))

	// Write-back assertion
	assert.Equal(t, "legacy-k", idem.lastKey,
		"middleware must write back to the same key during lazy migration")
	assert.Equal(t, fingerprintFor(requestBody), idem.lastFP,
		"write-back fingerprint must be the SHA-256 of the incoming body")
	require.NotNil(t, idem.lastIn, "Set must be called with the cached result preserved")
	assert.Equal(t, cachedBody, idem.lastIn.Body, "write-back preserves the cached body verbatim")
}

// TestIdempotency_2xxIsCached: a successful inner-handler response
// gets stored in the cache for replay.
func TestIdempotency_2xxIsCached(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{}
	r := newRouter(idem, successHandler)

	body := `{"x":1}`
	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k-2xx")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	require.NotNil(t, idem.lastIn, "2xx responses MUST be cached")
	assert.Equal(t, http.StatusAccepted, idem.lastIn.StatusCode)
	assert.JSONEq(t, `{"order_id":"abc","status":"processing"}`, idem.lastIn.Body,
		"cached body must be the captured response body verbatim")
	assert.Equal(t, fingerprintFor(body), idem.lastFP,
		"caching must store the request fingerprint")
}

// TestIdempotency_4xxNotCached: a 4xx response (validation OR
// business) must NOT be cached. Caching it would burn the key for
// 24h on a typo'd request OR pin a transient business error.
func TestIdempotency_4xxNotCached(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{}
	r := newRouter(idem, func(c *gin.Context) {
		c.Data(http.StatusConflict, "application/json", []byte(`{"error":"sold out"}`))
	})

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k-4xx")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Nil(t, idem.lastIn,
		"4xx MUST NOT be cached — would burn key for 24h on a transient/correctable error")
}

// TestIdempotency_5xxNotCached: deviation from Stripe's documented
// behaviour. Our 5xx mostly reflect transient (Redis blip) or
// programmer-error (unmapped error type) conditions; pinning them
// for 24h is worse customer experience than letting clients retry
// against a recovered server. nginx rate-limiting handles the
// retry-storm concern Stripe's policy was designed for.
func TestIdempotency_5xxNotCached(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{}
	r := newRouter(idem, func(c *gin.Context) {
		c.Data(http.StatusInternalServerError, "application/json", []byte(`{"error":"internal server error"}`))
	})

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k-5xx")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Nil(t, idem.lastIn,
		"5xx MUST NOT be cached — our 5xx are mostly transient; pinning for 24h hurts customer experience")
}

// TestIdempotency_CacheGetError_ProcessesNormally: Redis-down →
// fail-open. Inner handler runs, response is returned. Critical
// availability contract.
func TestIdempotency_CacheGetError_ProcessesNormally(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			return nil, "", errors.New("redis: connection refused")
		},
	}
	innerCalled := false
	r := newRouter(idem, func(c *gin.Context) {
		innerCalled = true
		successHandler(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "outage-k")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.True(t, innerCalled,
		"cache-Get error MUST fall through to inner handler — fail-open availability contract")
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// TestIdempotency_CacheGetError_SkipsSet: when cache-Get errored,
// the post-processing Set is skipped so a flaky-then-recovered Redis
// doesn't pin transient state for 24h.
func TestIdempotency_CacheGetError_SkipsSet(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			return nil, "", errors.New("redis: connection refused")
		},
	}
	r := newRouter(idem, successHandler)

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "outage-k-skip-set")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Nil(t, idem.lastIn,
		"when cache-Get errored, Set MUST be skipped — defends against transient-state pinning")
}

// TestIdempotency_FinalSetError_ResponseStillSent: a Redis write
// failure on the post-processing Set MUST NOT fail the
// already-committed HTTP response.
func TestIdempotency_FinalSetError_ResponseStillSent(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{
		setFn: func(_ context.Context, _ string, _ *domain.IdempotencyResult, _ string) error {
			return errors.New("redis: write failed")
		},
	}
	r := newRouter(idem, successHandler)

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "set-error-k")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code,
		"cache Set failure MUST NOT fail the request — response was already computed")
}

// TestIdempotency_LazyWriteBackError_ReplaysAnyway: a Redis blip on
// the legacy-migration write-back must not turn an
// otherwise-successful replay into an error response.
func TestIdempotency_LazyWriteBackError_ReplaysAnyway(t *testing.T) {
	t.Parallel()

	cachedBody := `{"order_id":"legacy"}`
	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			return &domain.IdempotencyResult{StatusCode: http.StatusAccepted, Body: cachedBody}, "", nil
		},
		setFn: func(_ context.Context, _ string, _ *domain.IdempotencyResult, _ string) error {
			return errors.New("redis: write-back failed")
		},
	}
	r := newRouter(idem, func(c *gin.Context) {
		t.Fatal("legacy replay must NOT call inner handler")
	})

	req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "legacy-flaky-redis")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code,
		"write-back failure MUST NOT block the replay — cached body is still valid")
	assert.JSONEq(t, cachedBody, w.Body.String())
	assert.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"))
}

// TestIdempotency_InvalidKey: bytes outside ASCII printable range or
// over 128 chars MUST short-circuit to 400 before any body read or
// cache lookup.
func TestIdempotency_InvalidKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{"control character (NUL)", "valid\x00prefix"},
		{"newline", "key-with-\nnewline"},
		{"non-ASCII (em dash)", "key—with-emdash"},
		{"too long (>128 chars)", strings.Repeat("a", 129)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			idem := &stubIdempotencyRepo{
				getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
					t.Fatal("invalid Idempotency-Key must short-circuit before cache lookup")
					return nil, "", nil
				},
			}
			r := newRouter(idem, func(c *gin.Context) {
				t.Fatal("invalid Idempotency-Key must short-circuit before inner handler")
			})

			req := httptest.NewRequest(http.MethodPost, "/book", strings.NewReader(`{"x":1}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", tt.key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "Idempotency-Key")
		})
	}
}
