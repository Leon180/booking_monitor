package booking_test

import (
	"context"
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
	getFn  func(ctx context.Context, key string) (*domain.IdempotencyResult, error)
	setFn  func(ctx context.Context, key string, result *domain.IdempotencyResult) error
	lastIn *domain.IdempotencyResult
}

func (s *stubIdempotencyRepo) Get(ctx context.Context, key string) (*domain.IdempotencyResult, error) {
	if s.getFn == nil {
		return nil, nil
	}
	return s.getFn(ctx, key)
}

func (s *stubIdempotencyRepo) Set(ctx context.Context, key string, r *domain.IdempotencyResult) error {
	s.lastIn = r
	if s.setFn == nil {
		return nil
	}
	return s.setFn(ctx, key, r)
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

// TestHandleBook_IdempotencyReplay verifies the cache-hit path:
// when the Idempotency-Key has a cached entry, the handler MUST
// return the cached body verbatim with X-Idempotency-Replayed: true
// AND the underlying BookTicket service is NOT called.
//
// Without this test, the cache-hit branch was untested — a future
// refactor that accidentally calls BookTicket BEFORE the cache
// lookup (or after, with a swallowed result) would silently break
// idempotency without any assertion firing.
func TestHandleBook_IdempotencyReplay(t *testing.T) {
	t.Parallel()

	cachedOrderID := uuid.New()
	cachedBody := `{"order_id":"` + cachedOrderID.String() + `","status":"processing","message":"booking accepted, awaiting confirmation","links":{"self":"/api/v1/orders/` + cachedOrderID.String() + `"}}`

	bookCalled := false
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			bookCalled = true
			return uuid.Nil, nil
		},
	}
	idem := &stubIdempotencyRepo{
		getFn: func(_ context.Context, key string) (*domain.IdempotencyResult, error) {
			assert.Equal(t, "replay-key", key)
			return &domain.IdempotencyResult{
				StatusCode: http.StatusAccepted,
				Body:       cachedBody,
			}, nil
		},
	}

	r := newRouter(svc, idem)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "replay-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.False(t, bookCalled, "BookTicket MUST NOT be called on idempotency cache hit")
	assert.Equal(t, http.StatusAccepted, w.Code, "replay must return the cached status code (202)")
	assert.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"),
		"X-Idempotency-Replayed: true header must be set on every replay")
	assert.JSONEq(t, cachedBody, w.Body.String(),
		"replay must return the cached body byte-for-byte (modulo JSON whitespace)")
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
