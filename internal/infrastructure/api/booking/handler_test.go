package booking_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/booking"
	"booking_monitor/internal/infrastructure/api/dto"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { gin.SetMode(gin.TestMode) }

// stubBookingService is a hand-rolled BookingService for boundary
// tests. We don't generate via mockgen because the surface is small
// and the hand-rolled version makes the per-test setup obvious at a
// glance — `bookFn` controls BookTicket, `getFn` controls GetOrder.
//
// Lives in the test package (booking_test) so the production binary
// never imports it; lives alongside the handler so adding fields is a
// one-file change.
type stubBookingService struct {
	bookFn    func(ctx context.Context, userID int, eventID uuid.UUID, qty int) (uuid.UUID, error)
	getFn     func(ctx context.Context, id uuid.UUID) (domain.Order, error)
	historyFn func(ctx context.Context, page, size int, status *domain.OrderStatus) ([]domain.Order, int, error)
}

func (s *stubBookingService) BookTicket(ctx context.Context, userID int, eventID uuid.UUID, qty int) (uuid.UUID, error) {
	if s.bookFn == nil {
		panic("BookTicket called without bookFn")
	}
	return s.bookFn(ctx, userID, eventID, qty)
}

func (s *stubBookingService) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	if s.getFn == nil {
		panic("GetOrder called without getFn")
	}
	return s.getFn(ctx, id)
}

func (s *stubBookingService) GetBookingHistory(ctx context.Context, page, size int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	if s.historyFn == nil {
		return nil, 0, nil
	}
	return s.historyFn(ctx, page, size, status)
}

// stubEventService is a no-op event service. This test file only
// exercises the booking + order paths; the event endpoints are
// covered separately and we don't want their failure modes to leak
// into these assertions.
type stubEventService struct{}

func (stubEventService) CreateEvent(_ context.Context, _ string, _ int) (domain.Event, error) {
	return domain.Event{}, errors.New("not used in this test")
}
func (stubEventService) GetEvent(_ context.Context, _ uuid.UUID) (domain.Event, error) {
	return domain.Event{}, errors.New("not used in this test")
}

// stubIdempotencyRepo lets each test control whether the in-memory
// cache hits or misses. The Set side records last-write so tests can
// verify the new response shape lands in the cache.
type stubIdempotencyRepo struct {
	getFn   func(ctx context.Context, key string) (*domain.IdempotencyResult, string, error)
	setFn   func(ctx context.Context, key string, result *domain.IdempotencyResult, fingerprint string) error
	lastIn  *domain.IdempotencyResult
	lastFP  string // fingerprint passed on the most recent Set
	lastKey string // key passed on the most recent Set
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

func newRouter(svc application.BookingService, idem domain.IdempotencyRepository) *gin.Engine {
	h := booking.NewBookingHandler(svc, stubEventService{}, idem)
	r := gin.New()
	v1 := r.Group("/api/v1")
	booking.RegisterRoutes(v1, h)
	return r
}

// TestHandleBook_AcceptedShape pins the canonical 202 response shape:
// status code, the new BookingAcceptedResponse fields (order_id,
// status, message, links.self), and that the same order_id flows into
// the idempotency cache record. Without this test, a future
// silent rename of `links.self` (e.g. to `poll_url`) ships and
// breaks every client that's been written against the documented
// shape.
func TestHandleBook_AcceptedShape(t *testing.T) {
	t.Parallel()

	wantOrderID := uuid.New()
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			return wantOrderID, nil
		},
	}
	idem := &stubIdempotencyRepo{}

	r := newRouter(svc, idem)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "smoke-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "POST /book success must return 202 Accepted")

	var got dto.BookingAcceptedResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, wantOrderID, got.OrderID, "response must echo the BookTicket-minted order_id")
	assert.Equal(t, dto.BookingStatusProcessing, got.Status,
		"status must be the typed BookingStatusProcessing constant — catches stringly-typed regressions")
	assert.Contains(t, got.Message, "booking accepted")
	assert.Equal(t, "/api/v1/orders/"+wantOrderID.String(), got.Links.Self,
		"links.self must be a valid GET /orders/:id URL")

	require.NotNil(t, idem.lastIn, "idempotency Set must be called when key is present")
	assert.Equal(t, http.StatusAccepted, idem.lastIn.StatusCode,
		"cached status code must match the wire response — 202, not 200")
}

// fingerprintFor mirrors the handler's hex-SHA256-of-raw-bytes
// fingerprinting so tests can pre-compute the value the handler
// would store. Centralised so a future hash algorithm change ripples
// through this single helper rather than every test fixture.
func fingerprintFor(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// TestHandleBook_IdempotencyReplay covers the same-key + same-body
// path: cached entry exists, fingerprint matches the incoming body,
// handler returns the cached body verbatim with
// X-Idempotency-Replayed: true and BookTicket is NOT called.
//
// N4 made fingerprint matching part of this contract — the test now
// pins both the cached-entry replay AND the fingerprint comparison
// returning a match.
func TestHandleBook_IdempotencyReplay(t *testing.T) {
	t.Parallel()

	cachedOrderID := uuid.New()
	cachedBody := `{"order_id":"` + cachedOrderID.String() + `","status":"processing","message":"booking accepted, awaiting confirmation","links":{"self":"/api/v1/orders/` + cachedOrderID.String() + `"}}`
	requestBody := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	matchingFP := fingerprintFor(requestBody)

	bookCalled := false
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			bookCalled = true
			return uuid.Nil, nil
		},
	}
	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, key string) (*domain.IdempotencyResult, string, error) {
			assert.Equal(t, "replay-key", key)
			return &domain.IdempotencyResult{
				StatusCode: http.StatusAccepted,
				Body:       cachedBody,
			}, matchingFP, nil
		},
	}

	r := newRouter(svc, idem)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "replay-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.False(t, bookCalled, "BookTicket MUST NOT be called on idempotency cache hit + fingerprint match")
	assert.Equal(t, http.StatusAccepted, w.Code, "replay must return the cached status code (202)")
	assert.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"),
		"X-Idempotency-Replayed: true header must be set on every replay")
	assert.JSONEq(t, cachedBody, w.Body.String(),
		"replay must return the cached body byte-for-byte (modulo JSON whitespace)")
}

// TestHandleBook_IdempotencyMismatch — Stripe's contract: same key +
// different body returns 409 Conflict and does NOT replay the cached
// response. Critical: returning the cached response in this case
// would let a client think their NEW request succeeded when in fact
// the original (different) request's response was returned.
func TestHandleBook_IdempotencyMismatch(t *testing.T) {
	t.Parallel()

	bookCalled := false
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			bookCalled = true
			return uuid.Nil, nil
		},
	}
	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			// Cached entry has a fingerprint for the ORIGINAL body —
			// the incoming body has a different one. The fingerprint
			// here is for some unrelated body; the handler will compute
			// a different value over the incoming body and detect the
			// mismatch.
			return &domain.IdempotencyResult{
				StatusCode: http.StatusAccepted,
				Body:       `{"order_id":"original","status":"processing"}`,
			}, fingerprintFor(`{"user_id":1,"event_id":"different","quantity":1}`), nil
		},
	}
	r := newRouter(svc, idem)

	// New body — fingerprint differs from cached entry
	newBody := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":99}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(newBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "shared-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.False(t, bookCalled, "BookTicket MUST NOT be called when fingerprint mismatch detected")
	assert.Equal(t, http.StatusConflict, w.Code,
		"same-key/different-body must return 409 Conflict (Stripe convention)")
	assert.Empty(t, w.Header().Get("X-Idempotency-Replayed"),
		"mismatch response is NOT a replay; the header must NOT be set")
	assert.Contains(t, w.Body.String(), "Idempotency-Key reused with a different request body",
		"public error message must explain the mismatch without leaking internals")
}

// TestHandleBook_LegacyEntryReplaysAndWritesBack covers the lazy-
// migration path: a pre-N4 cached entry has no fingerprint
// (stored before fingerprinting was introduced). The handler MUST
// replay (so existing clients don't see a regression at deploy time)
// AND write the new fingerprint back to Set so subsequent replays
// validate properly. Without the writeback, the migration window is
// 24h (cache TTL); with it, the window closes within milliseconds of
// the first replay.
func TestHandleBook_LegacyEntryReplaysAndWritesBack(t *testing.T) {
	t.Parallel()

	cachedBody := `{"order_id":"legacy","status":"processing"}`
	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			// Empty fingerprint = legacy entry signal
			return &domain.IdempotencyResult{
				StatusCode: http.StatusAccepted,
				Body:       cachedBody,
			}, "", nil
		},
	}
	bookCalled := false
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			bookCalled = true
			return uuid.Nil, nil
		},
	}
	r := newRouter(svc, idem)

	requestBody := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "legacy-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.False(t, bookCalled, "legacy replay MUST NOT call BookTicket — same as a normal replay")
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"))

	// Write-back assertion: handler must call Set with the freshly-
	// computed fingerprint so the next replay can validate.
	assert.Equal(t, "legacy-key", idem.lastKey,
		"handler must write back to the same key during lazy migration")
	assert.Equal(t, fingerprintFor(requestBody), idem.lastFP,
		"write-back fingerprint must be the SHA-256 of the incoming body — NOT the (empty) cached value")
	require.NotNil(t, idem.lastIn, "Set must be called with the cached result preserved")
	assert.Equal(t, cachedBody, idem.lastIn.Body, "write-back preserves the cached body verbatim")
}

// TestHandleBook_4xxNotCached pins the Stripe-style "do NOT cache 4xx
// validation errors" rule. Without it, a typo'd body would burn the
// idempotency key for 24h — a client correcting the typo and retrying
// with the same key would get the cached 400 forever instead of
// being able to legitimately complete the booking.
func TestHandleBook_4xxNotCached(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{}
	r := newRouter(&stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			t.Fatal("unparseable body should never reach the service")
			return uuid.Nil, nil
		},
	}, idem)

	// Malformed JSON — handler returns 400 from ShouldBindJSON
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(`{not-json`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "typo-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Nil(t, idem.lastIn,
		"4xx validation errors MUST NOT be cached — caching them burns the idempotency key for 24h")
}

// TestHandleBook_5xxIsCached pins the symmetric counterpart of the
// 4xx-not-cached rule: 5xx server errors ARE cached. Stripe's
// rationale — clients should NOT keep retrying a key that already
// produced a server error during the 24h window; the cached 5xx
// signals "this idempotent operation is in a degraded state, please
// stop hammering". Without this test, a future change to
// shouldCacheStatus that accidentally widens the exclusion (e.g.
// `status >= 500` flipped to `status > 500`) would silently leak.
func TestHandleBook_5xxIsCached(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{}
	// Return an unmapped error so mapError translates to 500.
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			return uuid.Nil, errors.New("internal: db connection refused")
		},
	}
	r := newRouter(svc, idem)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "key-5xx")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotNil(t, idem.lastIn,
		"5xx responses MUST be cached — Stripe convention; signals the client to stop retrying for 24h")
	assert.Equal(t, http.StatusInternalServerError, idem.lastIn.StatusCode,
		"the cached entry must carry the same 500 status the client received")
	assert.Equal(t, fingerprintFor(body), idem.lastFP,
		"5xx caching must store the request fingerprint so a same-body retry replays the same 500")
}

// TestHandleBook_FinalSetError_ResponseStillSent pins the contract
// that a Redis write failure on the final cache.Set MUST NOT fail the
// already-committed HTTP response. The handler logs WARN and falls
// through to c.Data — verified here so a future refactor that
// promotes the Set error to fail-loud doesn't regress the contract.
func TestHandleBook_FinalSetError_ResponseStillSent(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{
		setFn: func(_ context.Context, _ string, _ *domain.IdempotencyResult, _ string) error {
			return errors.New("redis: write failed")
		},
	}
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			return uuid.New(), nil
		},
	}
	r := newRouter(svc, idem)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "set-error-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code,
		"cache Set failure MUST NOT fail the request — response was already computed")
	assert.Contains(t, w.Body.String(), "order_id",
		"the 202 body must reach the client even when cache write failed")
}

// TestHandleBook_CacheGetError_ProcessesNormally pins the fail-open
// availability contract: when the idempotency cache GET fails (e.g.
// Redis outage), the handler MUST log ERROR and proceed to normal
// processing — NOT short-circuit with a 5xx. Without this test, a
// future refactor that promotes the Get error to "fail closed" would
// turn every Redis blip into a booking-endpoint outage; the failure
// mode is the precise scenario the
// `idempotency_cache_get_errors_total` counter exists to surface.
func TestHandleBook_CacheGetError_ProcessesNormally(t *testing.T) {
	t.Parallel()

	bookCalled := false
	wantOrderID := uuid.New()
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			bookCalled = true
			return wantOrderID, nil
		},
	}
	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			return nil, "", errors.New("redis: connection refused")
		},
	}
	r := newRouter(svc, idem)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "outage-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.True(t, bookCalled, "cache-Get error MUST fall through to BookTicket — fail-open availability contract")
	assert.Equal(t, http.StatusAccepted, w.Code,
		"cache-Get error MUST NOT block the request — Redis outage shouldn't outage the booking endpoint")
	assert.Contains(t, w.Body.String(), wantOrderID.String(),
		"successful response shape must reach the client even with idempotency suspended")
}

// TestHandleBook_CacheGetError_SkipsSet pins the transient-5xx
// mitigation: when the cache-GET errored, the subsequent Set MUST be
// skipped. Otherwise a flaky-then-recovered Redis would cache a
// transient 5xx response for 24h — same-key retries would then replay
// the cached failure rather than re-attempting against a healthy
// dependency. Stripe's "cache 5xx to prevent retry storm" assumes the
// 5xx represents a stable condition; a transient cache-layer outage
// violates that assumption.
func TestHandleBook_CacheGetError_SkipsSet(t *testing.T) {
	t.Parallel()

	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			return nil, "", errors.New("redis: connection refused")
		},
	}
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			return uuid.New(), nil
		},
	}
	r := newRouter(svc, idem)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "outage-key-skip-set")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code, "request must succeed even though cache is down")
	assert.Nil(t, idem.lastIn,
		"when cache-Get errored, Set MUST be skipped — caching a response written through a flaky-then-recovered Redis risks pinning transient failures for 24h")
}

// TestHandleBook_LazyWriteBackError_ReplaysAnyway pins the same
// fail-open contract for the legacy-migration write-back path. A
// Redis blip during write-back must not turn an otherwise-successful
// replay into an error response — the cached body is still valid.
func TestHandleBook_LazyWriteBackError_ReplaysAnyway(t *testing.T) {
	t.Parallel()

	cachedBody := `{"order_id":"legacy","status":"processing"}`
	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
			return &domain.IdempotencyResult{
				StatusCode: http.StatusAccepted,
				Body:       cachedBody,
			}, "", nil // empty fp = legacy entry
		},
		setFn: func(_ context.Context, _ string, _ *domain.IdempotencyResult, _ string) error {
			return errors.New("redis: write-back failed")
		},
	}
	r := newRouter(&stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			t.Fatal("legacy replay must NOT call BookTicket")
			return uuid.Nil, nil
		},
	}, idem)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "legacy-flaky-redis")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code,
		"write-back failure MUST NOT block the replay — cached body is still valid")
	assert.JSONEq(t, cachedBody, w.Body.String())
	assert.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"))
}

// TestHandleBook_InvalidIdempotencyKey covers the Idempotency-Key
// character validation gap closed in N4. Pre-N4 the only check was
// length; control characters could embed and confuse downstream log
// parsers, terminal output, etc. The handler now rejects any key
// containing bytes outside ASCII printable range (0x20-0x7E).
func TestHandleBook_InvalidIdempotencyKey(t *testing.T) {
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

			r := newRouter(&stubBookingService{
				bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
					t.Fatal("invalid Idempotency-Key must short-circuit before service call")
					return uuid.Nil, nil
				},
			}, &stubIdempotencyRepo{})

			body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", tt.key)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "Idempotency-Key")
		})
	}
}

// TestHandleBook_SoldOut pins the 409 sold-out error path. Without
// it, the status-code change (200 → 202 in PR #47) leaves the error
// path's status code unverified — a typo in mapError or in the
// error-path branch could silently turn sold-out responses into
// 200 OK.
func TestHandleBook_SoldOut(t *testing.T) {
	t.Parallel()

	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			return uuid.Nil, domain.ErrSoldOut
		},
	}
	idem := &stubIdempotencyRepo{}
	r := newRouter(svc, idem)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code, "sold-out must surface as 409 Conflict")
	var got dto.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "sold out", got.Error,
		"public error message must be the sanitized 'sold out' string from mapError")
}

// TestHandleGetOrder_OK verifies the polling endpoint returns the
// canonical OrderResponse shape. Pins the route binding (POST→GET)
// and the DTO mapper invocation in one shot.
func TestHandleGetOrder_OK(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	eventID := uuid.New()
	created := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)
	svc := &stubBookingService{
		getFn: func(_ context.Context, gotID uuid.UUID) (domain.Order, error) {
			assert.Equal(t, id, gotID, "handler must propagate the path-param uuid verbatim")
			return domain.ReconstructOrder(id, 7, eventID, 2, domain.OrderStatusConfirmed, created), nil
		},
	}
	r := newRouter(svc, &stubIdempotencyRepo{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+id.String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got dto.OrderResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, id, got.ID)
	assert.Equal(t, eventID, got.EventID)
	assert.Equal(t, 7, got.UserID)
	assert.Equal(t, 2, got.Quantity)
	assert.Equal(t, "confirmed", got.Status)
}

// TestHandleGetOrder_NotFound documents the 404 contract: GET /orders/:id
// returns 404 when the worker has not yet persisted the order. This
// is the brief sub-second window between `POST /book` (202) and the
// async worker's DB INSERT — clients are expected to retry with
// backoff. We use a structured ErrorResponse so the client can
// distinguish from other 4xx without parsing prose.
func TestHandleGetOrder_NotFound(t *testing.T) {
	t.Parallel()

	svc := &stubBookingService{
		getFn: func(_ context.Context, _ uuid.UUID) (domain.Order, error) {
			return domain.Order{}, domain.ErrOrderNotFound
		},
	}
	r := newRouter(svc, &stubIdempotencyRepo{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+uuid.New().String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code,
		"during the async-processing window, GET must 404 — clients retry with backoff")
	assert.Contains(t, w.Body.String(), "order not found")
}

// TestHandleGetOrder_InvalidUUID guards against the path param being
// absent / malformed. Without explicit handling, uuid.Parse failures
// would propagate as 500 — but a malformed path param is squarely a
// client error.
func TestHandleGetOrder_InvalidUUID(t *testing.T) {
	t.Parallel()

	svc := &stubBookingService{
		getFn: func(_ context.Context, _ uuid.UUID) (domain.Order, error) {
			t.Fatal("GetOrder must not be called for a malformed path param")
			return domain.Order{}, nil
		},
	}
	r := newRouter(svc, &stubIdempotencyRepo{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/not-a-uuid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid order id")
}
