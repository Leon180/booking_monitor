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

// stubBookingService is a hand-rolled BookingService for handler
// boundary tests. The surface is small and the hand-rolled version
// makes the per-test setup obvious at a glance — `bookFn` controls
// BookTicket, `getFn` controls GetOrder.
//
// Lives in booking_test (not the production binary) so its panics on
// nil callbacks are test-time-only failures.
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

type stubEventService struct{}

func (stubEventService) CreateEvent(_ context.Context, _ string, _ int) (domain.Event, error) {
	return domain.Event{}, errors.New("not used in this test")
}
func (stubEventService) GetEvent(_ context.Context, _ uuid.UUID) (domain.Event, error) {
	return domain.Event{}, errors.New("not used in this test")
}

// noopIdempotencyRepo is the zero-behaviour repo passed to the route
// registration. The booking handler test file does NOT exercise the
// idempotency contract — that lives in
// internal/infrastructure/api/middleware/idempotency_test.go. The
// requests in this file all OMIT the Idempotency-Key header so the
// middleware passes through; the repo is wired in only because
// RegisterRoutes accepts it.
type noopIdempotencyRepo struct{}

func (noopIdempotencyRepo) Get(_ context.Context, _ string) (*domain.IdempotencyResult, string, error) {
	return nil, "", nil
}
func (noopIdempotencyRepo) Set(_ context.Context, _ string, _ *domain.IdempotencyResult, _ string) error {
	return nil
}

func newRouter(svc application.BookingService) *gin.Engine {
	h := booking.NewBookingHandler(svc, stubEventService{})
	r := gin.New()
	v1 := r.Group("/api/v1")
	booking.RegisterRoutes(v1, h, noopIdempotencyRepo{})
	return r
}

// TestHandleBook_AcceptedShape pins the canonical 202 response shape:
// status code, the BookingAcceptedResponse fields (order_id, status,
// message, links.self), without any idempotency-cache plumbing.
//
// Without this test, a silent rename of `links.self` (e.g. to
// `poll_url`) ships and breaks every client written against the
// documented shape.
func TestHandleBook_AcceptedShape(t *testing.T) {
	t.Parallel()

	wantOrderID := uuid.New()
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			return wantOrderID, nil
		},
	}
	r := newRouter(svc)

	body := `{"user_id":1,"event_id":"` + uuid.New().String() + `","quantity":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/book", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
}

// TestHandleBook_SoldOut pins the 409 sold-out path. Status code +
// sanitized error message via mapError.
func TestHandleBook_SoldOut(t *testing.T) {
	t.Parallel()

	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (uuid.UUID, error) {
			return uuid.Nil, domain.ErrSoldOut
		},
	}
	r := newRouter(svc)

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
	r := newRouter(svc)

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
	r := newRouter(svc)

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
	r := newRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/not-a-uuid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "invalid order id")
}

// TestHandleListBookings_SizeIsCapped guards against `?size=BIG`
// causing a full-table scan + huge slice allocation in one request
// (S1 from the Phase 2 checkpoint). The test asserts the service is
// invoked with the capped value (100) regardless of how large the
// query string asks for. Without this test, a refactor that removed
// the cap would silently re-open the resource-exhaustion vector.
func TestHandleListBookings_SizeIsCapped(t *testing.T) {
	t.Parallel()

	const cap = 100
	var observedSize int
	svc := &stubBookingService{
		historyFn: func(_ context.Context, _, size int, _ *domain.OrderStatus) ([]domain.Order, int, error) {
			observedSize = size
			return []domain.Order{}, 0, nil
		},
	}
	r := newRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?page=1&size=1000000", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, cap, observedSize, "page size MUST be capped at %d to prevent unbounded scans", cap)
}
