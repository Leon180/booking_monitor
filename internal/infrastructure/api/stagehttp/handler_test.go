package stagehttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"
	"booking_monitor/internal/infrastructure/api/stagehttp"
)

// Unit tests for stagehttp handlers. Shape-only — the SQL semantics
// of HandlePayIntent + HandleTestConfirm's UPDATE paths live in:
//
//   - test/integration/postgres/stagehttp_pay_test.go      (5 tests, /pay)
//   - test/integration/postgres/stagehttp_confirm_test.go  (9 tests, /confirm)
//
// Both are testcontainers-backed against a real postgres:15-alpine
// container. They pin the SQL predicates and side effects this
// file's fakes can't reach (atomic `payment_intent_id IS NOT NULL`
// guard, atomic `reserved_until > NOW()` TTL guard, /pay-first
// pre-check before Compensator, etc.).
//
// These unit tests cover what the integration tests can't reach:
//   - Status code mapping for booking.Service errors via MapBookingError
//   - Response body shape (BookingAcceptedResponse for 202, error JSON for 4xx)
//   - Handler-level guards that reject before any DB call (invalid UUID → 400, missing outcome → 400)
//   - Compensator sentinel mapping (ErrCompensateNotEligible → 409)
//
// Mocks rather than gomock — fakeService / fakeCompensator are
// readable in-place; the interface surface is small (3 methods +
// 1 method respectively). Same convention as the existing
// internal/application/booking/service_test.go style.

// ────────────────────────────────────────────────────────────────
// Fakes
// ────────────────────────────────────────────────────────────────

type fakeService struct {
	bookFn   func(ctx context.Context, userID int, ttID uuid.UUID, qty int) (domain.Order, error)
	getFn    func(ctx context.Context, id uuid.UUID) (domain.Order, error)
	listFn   func(ctx context.Context, page, size int, status *domain.OrderStatus) ([]domain.Order, int, error)
}

func (f *fakeService) BookTicket(ctx context.Context, userID int, ttID uuid.UUID, qty int) (domain.Order, error) {
	return f.bookFn(ctx, userID, ttID, qty)
}
func (f *fakeService) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return f.getFn(ctx, id)
}
func (f *fakeService) GetBookingHistory(ctx context.Context, page, size int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	return f.listFn(ctx, page, size, status)
}

type fakeCompensator struct {
	fn func(ctx context.Context, orderID uuid.UUID) error
}

func (f *fakeCompensator) Compensate(ctx context.Context, orderID uuid.UUID) error {
	return f.fn(ctx, orderID)
}

// validBookingRequest produces a valid JSON body for HandleBook.
func validBookingRequest(t *testing.T) []byte {
	t.Helper()
	ttID, err := uuid.NewV7()
	require.NoError(t, err)
	body, err := json.Marshal(dto.BookingRequest{
		UserID:       42,
		TicketTypeID: ttID,
		Quantity:     1,
	})
	require.NoError(t, err)
	return body
}

// validReservation builds a valid Pattern A reservation domain.Order
// for fake-service returns. ReservedUntil is 15 minutes future so the
// expires_in_seconds + reserved_until response fields are non-zero.
func validReservation(t *testing.T) domain.Order {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	eventID := uuid.New()
	ttID, err := uuid.NewV7()
	require.NoError(t, err)
	order, err := domain.NewReservation(id, 42, eventID, ttID, 1,
		time.Now().Add(15*time.Minute), 2000, "usd")
	require.NoError(t, err)
	return order
}

// newGinRouter spins up a Gin router with a single route mounted via
// fn. Avoids gin's default middleware so test failures are deterministic.
func newGinRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	return r
}

// ────────────────────────────────────────────────────────────────
// MapBookingError
// ────────────────────────────────────────────────────────────────

func TestMapBookingError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantMsgSub string // substring assertion (allows wrapped errors)
	}{
		{"TicketTypeNotFound", domain.ErrTicketTypeNotFound, http.StatusNotFound, "ticket_type"},
		{"OrderNotFound", domain.ErrOrderNotFound, http.StatusNotFound, "order"},
		{"SoldOut", domain.ErrSoldOut, http.StatusConflict, "sold out"},
		{"UserAlreadyBought", domain.ErrUserAlreadyBought, http.StatusConflict, "already booked"},
		{"CompensateNotEligible", stagehttp.ErrCompensateNotEligible, http.StatusConflict, "awaiting_payment"},
		{"InvalidUserID", domain.ErrInvalidUserID, http.StatusBadRequest, "invalid request"},
		{"InvalidQuantity", domain.ErrInvalidQuantity, http.StatusBadRequest, "invalid request"},
		{"InvalidTicketTypeID", domain.ErrInvalidOrderTicketTypeID, http.StatusBadRequest, "invalid request"},
		{"WrappedSoldOut", errors.New("wrapped: " + domain.ErrSoldOut.Error()), http.StatusInternalServerError, "internal error"},
		// Wrapped error with errors.Is preserved → mapped to sentinel
		{"FmtWrappedSoldOut", wrapErr(domain.ErrSoldOut), http.StatusConflict, "sold out"},
		{"UnknownError", errors.New("some unknown thing"), http.StatusInternalServerError, "internal error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := stagehttp.MapBookingError(tc.err)
			assert.Equal(t, tc.wantStatus, status)
			assert.Contains(t, msg, tc.wantMsgSub)
		})
	}
}

// wrapErr wraps an error with fmt.Errorf %w so errors.Is still
// matches. Sanity-checks MapBookingError's errors.Is usage.
func wrapErr(inner error) error {
	return wrappedErr{inner: inner}
}

type wrappedErr struct{ inner error }

func (w wrappedErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w wrappedErr) Unwrap() error { return w.inner }

// ────────────────────────────────────────────────────────────────
// HandleBook
// ────────────────────────────────────────────────────────────────

func TestHandleBook_Success(t *testing.T) {
	order := validReservation(t)
	svc := &fakeService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (domain.Order, error) {
			return order, nil
		},
	}
	r := newGinRouter()
	r.POST("/api/v1/book", stagehttp.HandleBook(svc))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/book",
		bytes.NewReader(validBookingRequest(t)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp dto.BookingAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, order.ID(), resp.OrderID)
	assert.Equal(t, dto.BookingStatusReserved, resp.Status)
	assert.True(t, resp.ReservedUntil.After(time.Now()),
		"reserved_until should be in the future")
	assert.Greater(t, resp.ExpiresInSeconds, 0)
	assert.Equal(t, "/api/v1/orders/"+order.ID().String(), resp.Links.Self)
	assert.Equal(t, "/api/v1/orders/"+order.ID().String()+"/pay", resp.Links.Pay)
}

func TestHandleBook_ServiceErrors(t *testing.T) {
	cases := []struct {
		name       string
		serviceErr error
		wantStatus int
	}{
		{"sold_out", domain.ErrSoldOut, http.StatusConflict},
		{"already_booked", domain.ErrUserAlreadyBought, http.StatusConflict},
		{"ticket_type_missing", domain.ErrTicketTypeNotFound, http.StatusNotFound},
		{"invalid_user", domain.ErrInvalidUserID, http.StatusBadRequest},
		{"unexpected", errors.New("driver state corrupted"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{
				bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (domain.Order, error) {
					return domain.Order{}, tc.serviceErr
				},
			}
			r := newGinRouter()
			r.POST("/api/v1/book", stagehttp.HandleBook(svc))

			req := httptest.NewRequest(http.MethodPost, "/api/v1/book",
				bytes.NewReader(validBookingRequest(t)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code,
				"body=%s", rec.Body.String())
		})
	}
}

func TestHandleBook_BindingErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"invalid_json", `{not json`},
		{"missing_user_id", `{"ticket_type_id":"019e0000-0000-7000-8000-000000000000","quantity":1}`},
		{"zero_user_id", `{"user_id":0,"ticket_type_id":"019e0000-0000-7000-8000-000000000000","quantity":1}`},
		{"qty_too_high", `{"user_id":1,"ticket_type_id":"019e0000-0000-7000-8000-000000000000","quantity":11}`},
	}
	svc := &fakeService{
		// Should NOT be called — binding rejects before service.
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (domain.Order, error) {
			t.Fatalf("service should not be called on binding failure")
			return domain.Order{}, nil
		},
	}
	r := newGinRouter()
	r.POST("/api/v1/book", stagehttp.HandleBook(svc))

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/book",
				strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code,
				"body=%s", rec.Body.String())
		})
	}
}

// ────────────────────────────────────────────────────────────────
// HandleGetOrder
// ────────────────────────────────────────────────────────────────

func TestHandleGetOrder_Success(t *testing.T) {
	order := validReservation(t)
	svc := &fakeService{
		getFn: func(_ context.Context, _ uuid.UUID) (domain.Order, error) {
			return order, nil
		},
	}
	r := newGinRouter()
	r.GET("/api/v1/orders/:id", stagehttp.HandleGetOrder(svc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+order.ID().String(), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, order.ID().String(), body["order_id"])
	assert.Equal(t, string(order.Status()), body["status"])
}

func TestHandleGetOrder_NotFound(t *testing.T) {
	svc := &fakeService{
		getFn: func(_ context.Context, _ uuid.UUID) (domain.Order, error) {
			return domain.Order{}, domain.ErrOrderNotFound
		},
	}
	r := newGinRouter()
	r.GET("/api/v1/orders/:id", stagehttp.HandleGetOrder(svc))

	id, _ := uuid.NewV7()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+id.String(), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleGetOrder_InvalidID(t *testing.T) {
	svc := &fakeService{
		getFn: func(_ context.Context, _ uuid.UUID) (domain.Order, error) {
			t.Fatal("service should not be called on invalid id")
			return domain.Order{}, nil
		},
	}
	r := newGinRouter()
	r.GET("/api/v1/orders/:id", stagehttp.HandleGetOrder(svc))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// ────────────────────────────────────────────────────────────────
// HandleTestConfirm — non-SQL paths only
// ────────────────────────────────────────────────────────────────
//
// The SQL paths (succeeded UPDATE atomic predicates, failed
// Compensator orchestration, /pay-first pre-check, sentinel
// mapping for ErrCompensateNotEligible vs generic errors) are
// covered by test/integration/postgres/stagehttp_confirm_test.go
// against a real postgres:15-alpine container with a recording
// fakeCompensator. These unit tests cover what that file can't:
// invalid input rejection BEFORE any DB call (invalid UUID, missing/
// unknown outcome) — exercising those there would just mean
// spinning up a container per case for no DB-side coverage.

func TestHandleTestConfirm_InvalidUUID(t *testing.T) {
	r := newGinRouter()
	// nil DB + nil compensator is OK because invalid UUID rejects
	// before either is touched. Verify by setting them to fake
	// implementations that fail loudly if called.
	comp := &fakeCompensator{fn: func(_ context.Context, _ uuid.UUID) error {
		t.Fatal("compensator should not be called on invalid uuid")
		return nil
	}}
	r.POST("/test/payment/confirm/:id", stagehttp.HandleTestConfirm(nil, comp))

	req := httptest.NewRequest(http.MethodPost, "/test/payment/confirm/not-a-uuid?outcome=failed", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleTestConfirm_InvalidOutcome(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"missing_outcome", ""},
		{"unknown_outcome", "?outcome=maybe"},
		{"empty_outcome", "?outcome="},
	}
	comp := &fakeCompensator{fn: func(_ context.Context, _ uuid.UUID) error {
		t.Fatal("compensator should not be called on invalid outcome")
		return nil
	}}
	r := newGinRouter()
	r.POST("/test/payment/confirm/:id", stagehttp.HandleTestConfirm(nil, comp))

	id, _ := uuid.NewV7()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/test/payment/confirm/"+id.String()+tc.query, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code,
				"body=%s", rec.Body.String())
		})
	}
}
