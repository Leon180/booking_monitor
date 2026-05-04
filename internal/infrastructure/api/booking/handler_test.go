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
	bookingapp "booking_monitor/internal/application/booking"
	paymentapp "booking_monitor/internal/application/payment"
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
//
// D4.1 contract pin: BookTicket's second UUID arg is `ticketTypeID`,
// NOT `eventID` (signature changed PR-D4.1). The compile-time
// `var _ bookingapp.Service = (*stubBookingService)(nil)` assertion
// below catches future signature drift — a stub that goes stale would
// otherwise compile silently because both arg shapes are uuid.UUID.
type stubBookingService struct {
	bookFn    func(ctx context.Context, userID int, ticketTypeID uuid.UUID, qty int) (domain.Order, error)
	getFn     func(ctx context.Context, id uuid.UUID) (domain.Order, error)
	historyFn func(ctx context.Context, page, size int, status *domain.OrderStatus) ([]domain.Order, int, error)
}

// Compile-time assertion that the stub implements the production
// interface. Without this, a future signature change to
// bookingapp.Service would compile here silently because Go's
// structural typing accepts any (uuid.UUID, uuid.UUID) ordering.
var _ bookingapp.Service = (*stubBookingService)(nil)

func (s *stubBookingService) BookTicket(ctx context.Context, userID int, ticketTypeID uuid.UUID, qty int) (domain.Order, error) {
	if s.bookFn == nil {
		panic("BookTicket called without bookFn")
	}
	return s.bookFn(ctx, userID, ticketTypeID, qty)
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

// stubEventService mirrors stubBookingService — controllable via per-test
// fn fields so each handler test pins one path. Tests that don't touch
// the event surface leave createFn nil; if HandleCreateEvent fires on
// such a test, the fallback returns a sentinel error to surface the
// missing setup.
//
// Why no GetEvent method here: event.Service today only declares
// CreateEvent (HandleViewEvent is a stub that returns gin.H without
// hitting the service — see docs/checkpoints/20260430-phase2-review.md
// item A4). Adding a no-op GetEvent stub here would silently absorb a
// future event.Service interface expansion — the compile error is the
// useful signal.
type stubEventService struct {
	createFn func(ctx context.Context, name string, totalTickets int) (domain.Event, error)
}

func (s *stubEventService) CreateEvent(ctx context.Context, name string, totalTickets int) (domain.Event, error) {
	if s.createFn == nil {
		return domain.Event{}, errors.New("CreateEvent called without createFn")
	}
	return s.createFn(ctx, name, totalTickets)
}

// stubPaymentService is the D4 counterpart to stubBookingService /
// stubEventService — controllable via per-test fn fields. Implements
// the full payment.Service interface (ProcessOrder + CreatePaymentIntent)
// even though only handler_test.go's /pay tests exercise the latter.
// Unset fn fields fall back to "this method wasn't expected for this
// test" sentinel errors so missing setup surfaces loudly.
type stubPaymentService struct {
	processFn       func(ctx context.Context, event *application.OrderCreatedEvent) error
	createIntentFn  func(ctx context.Context, orderID uuid.UUID) (domain.PaymentIntent, error)
}

func (s *stubPaymentService) ProcessOrder(ctx context.Context, event *application.OrderCreatedEvent) error {
	if s.processFn == nil {
		return errors.New("ProcessOrder called without processFn")
	}
	return s.processFn(ctx, event)
}

func (s *stubPaymentService) CreatePaymentIntent(ctx context.Context, orderID uuid.UUID) (domain.PaymentIntent, error) {
	if s.createIntentFn == nil {
		return domain.PaymentIntent{}, errors.New("CreatePaymentIntent called without createIntentFn")
	}
	return s.createIntentFn(ctx, orderID)
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

func newRouter(svc bookingapp.Service) *gin.Engine {
	return newRouterWith(svc, &stubEventService{}, &stubPaymentService{})
}

// newRouterWithEvents is the variant for tests that need to drive the
// event handler. Three-arg form delegates to the canonical
// newRouterWith helper with a stub payment service.
func newRouterWithEvents(svc bookingapp.Service, evt *stubEventService) *gin.Engine {
	return newRouterWith(svc, evt, &stubPaymentService{})
}

// newRouterWith is the canonical wiring used by every router-helper
// in the test file. Each handler-dependency is a stub the test can
// drive selectively (any unset fn fields panic on call — surfaces
// missing setup loudly). Added in D4 alongside HandleCreatePaymentIntent.
func newRouterWith(svc bookingapp.Service, evt *stubEventService, pay *stubPaymentService) *gin.Engine {
	h := booking.NewBookingHandler(svc, evt, pay)
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

	wantOrderID := uuid.Must(uuid.NewV7())
	wantEventID := uuid.New()
	// D3 (Pattern A): the handler now expects an Order with reservedUntil
	// set. We pin a specific TTL so the response-side assertions can
	// match exactly rather than against gomock-Any-style tolerances.
	wantReservedUntil := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second)
	wantOrder := domain.ReconstructOrder(wantOrderID, 1, wantEventID, uuid.Nil, 1, domain.OrderStatusAwaitingPayment, time.Now(), wantReservedUntil, "", 0, "")
	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (domain.Order, error) {
			return wantOrder, nil
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
	assert.Equal(t, dto.BookingStatusReserved, got.Status,
		"D3: status must be 'reserved' (not 'processing') — Pattern A response vocabulary")
	assert.Contains(t, got.Message, "reservation accepted")
	assert.WithinDuration(t, wantReservedUntil, got.ReservedUntil, time.Second,
		"reserved_until must round-trip as RFC3339 UTC; 1s tolerance for JSON encode-decode rounding")
	assert.True(t, got.ExpiresInSeconds > 0 && got.ExpiresInSeconds <= 15*60,
		"expires_in_seconds must be a positive duration ≤ window; got %d", got.ExpiresInSeconds)
	assert.Equal(t, "/api/v1/orders/"+wantOrderID.String(), got.Links.Self,
		"links.self must be a valid GET /orders/:id URL")
	assert.Equal(t, "/api/v1/orders/"+wantOrderID.String()+"/pay", got.Links.Pay,
		"D3: links.pay must point at the D4 payment endpoint — clients use this to initiate Stripe checkout")
}

// TestHandleBook_SoldOut pins the 409 sold-out path. Status code +
// sanitized error message via mapError.
func TestHandleBook_SoldOut(t *testing.T) {
	t.Parallel()

	svc := &stubBookingService{
		bookFn: func(_ context.Context, _ int, _ uuid.UUID, _ int) (domain.Order, error) {
			return domain.Order{}, domain.ErrSoldOut
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
			return domain.ReconstructOrder(id, 7, eventID, uuid.Nil, 2, domain.OrderStatusConfirmed, created, time.Time{}, "", 0, ""), nil
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

// TestHandleListBookings_DefaultsApply: ?page= empty + ?size= empty
// → handler defaults to page=1, size=10. Pins the default contract
// so a refactor that drops one default doesn't silently change pagination.
func TestHandleListBookings_DefaultsApply(t *testing.T) {
	t.Parallel()

	var observedPage, observedSize int
	svc := &stubBookingService{
		historyFn: func(_ context.Context, page, size int, _ *domain.OrderStatus) ([]domain.Order, int, error) {
			observedPage, observedSize = page, size
			return []domain.Order{}, 0, nil
		},
	}
	r := newRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/history", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, observedPage, "missing page query param must default to 1")
	assert.Equal(t, 10, observedSize, "missing size query param must default to 10")
}

// TestHandleListBookings_StatusFilter: ?status=confirmed propagates as
// a non-nil pointer to the service. Pins the parsing contract.
func TestHandleListBookings_StatusFilter(t *testing.T) {
	t.Parallel()

	var observedStatus *domain.OrderStatus
	svc := &stubBookingService{
		historyFn: func(_ context.Context, _, _ int, status *domain.OrderStatus) ([]domain.Order, int, error) {
			observedStatus = status
			return []domain.Order{}, 0, nil
		},
	}
	r := newRouter(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?status=confirmed", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, observedStatus, "?status=confirmed must propagate as non-nil pointer")
	assert.Equal(t, domain.OrderStatusConfirmed, *observedStatus)
}

// TestHandleCreateEvent_HappyPath: POST /events with a valid body
// returns 201 + EventResponse echoing the service's rehydrated Event.
func TestHandleCreateEvent_HappyPath(t *testing.T) {
	t.Parallel()

	wantID := uuid.New()
	created := domain.ReconstructEvent(wantID, "Concert", 100, 100, 0)
	bookSvc := &stubBookingService{}
	evt := &stubEventService{
		createFn: func(_ context.Context, name string, total int) (domain.Event, error) {
			assert.Equal(t, "Concert", name)
			assert.Equal(t, 100, total)
			return created, nil
		},
	}
	r := newRouterWithEvents(bookSvc, evt)

	body := `{"name":"Concert","total_tickets":100}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "POST /events must return 201 Created on success")
	var got dto.EventResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, wantID, got.ID, "response must echo the service-minted event id")
	assert.Equal(t, "Concert", got.Name)
	assert.Equal(t, 100, got.TotalTickets,
		"DTO must echo total_tickets — guards against a name/totalTickets arg-swap regression at the service call site")
	assert.Equal(t, 100, got.AvailableTickets, "fresh event must have AvailableTickets == TotalTickets")
}

// TestHandleCreateEvent_InvalidBody: malformed JSON → 400 from the
// Gin binder, before the service is ever called.
func TestHandleCreateEvent_InvalidBody(t *testing.T) {
	t.Parallel()

	bookSvc := &stubBookingService{}
	evt := &stubEventService{
		createFn: func(_ context.Context, _ string, _ int) (domain.Event, error) {
			t.Fatal("CreateEvent must not be called when the body fails to bind")
			return domain.Event{}, nil
		},
	}
	r := newRouterWithEvents(bookSvc, evt)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, "malformed JSON must surface as 400")
	assert.Contains(t, w.Body.String(), "invalid request body")
}

// TestHandleCreateEvent_ServiceError: a generic service error falls
// through to mapError's default branch → 500 with sanitized message.
// Pins the never-leak-internals contract: the public message must NOT
// contain the underlying driver / network text.
func TestHandleCreateEvent_ServiceError(t *testing.T) {
	t.Parallel()

	bookSvc := &stubBookingService{}
	evt := &stubEventService{
		createFn: func(_ context.Context, _ string, _ int) (domain.Event, error) {
			return domain.Event{}, errors.New("postgres: relation \"events\" does not exist")
		},
	}
	r := newRouterWithEvents(bookSvc, evt)

	body := `{"name":"Concert","total_tickets":100}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	respBody := w.Body.String()
	assert.Contains(t, respBody, "internal server error")
	assert.NotContains(t, respBody, "postgres",
		"public error must NOT leak driver / SQL text")
}

// TestHandleViewEvent: the endpoint echoes the path param and bumps
// the page-views counter. We verify the response shape — counter
// observability is exercised at the metrics layer.
func TestHandleViewEvent(t *testing.T) {
	t.Parallel()

	r := newRouter(&stubBookingService{})

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/"+id.String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, id.String(), got["event_id"], "handler must echo the path-param id verbatim")
	assert.Equal(t, "View event", got["message"])
}

// TestHandlePay_Success pins the canonical 200 response shape on /pay.
// Status code + every PaymentIntentResponse field. Without this, a
// silent rename of `client_secret` (Stripe Elements would break) or
// `amount_cents` (UI countdown displays would break) ships unnoticed.
func TestHandlePay_Success(t *testing.T) {
	t.Parallel()

	orderID := uuid.Must(uuid.NewV7())
	wantIntent := domain.PaymentIntent{
		ID:           "pi_3test_abcdef",
		ClientSecret: "pi_3test_abcdef_secret_xyz",
		AmountCents:  4000,
		Currency:     "usd",
	}
	pay := &stubPaymentService{
		createIntentFn: func(_ context.Context, gotID uuid.UUID) (domain.PaymentIntent, error) {
			assert.Equal(t, orderID, gotID, "handler must propagate path-param uuid verbatim")
			return wantIntent, nil
		},
	}
	r := newRouterWith(&stubBookingService{}, &stubEventService{}, pay)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+orderID.String()+"/pay", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "/pay success must return 200 OK")
	var got dto.PaymentIntentResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, orderID, got.OrderID, "response must echo the path-param order_id")
	assert.Equal(t, "pi_3test_abcdef", got.PaymentIntentID, "payment_intent_id must round-trip from gateway")
	assert.Equal(t, "pi_3test_abcdef_secret_xyz", got.ClientSecret, "client_secret must round-trip; clients use it with Stripe Elements")
	assert.Equal(t, int64(4000), got.AmountCents)
	assert.Equal(t, "usd", got.Currency)
}

// TestHandlePay_InvalidUUID: path-level UUID parse failure → 400.
// Defense-in-depth even though Gin's path-param matching mostly
// catches this; a future router change that routes ":id" more
// loosely shouldn't slip through to the service.
func TestHandlePay_InvalidUUID(t *testing.T) {
	t.Parallel()

	r := newRouterWith(&stubBookingService{}, &stubEventService{}, &stubPaymentService{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/not-a-uuid/pay", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, "invalid UUID in path must surface as 400")
}

// TestHandlePay_OrderNotFound: 404 when service returns ErrOrderNotFound.
// Pins the mapError translation so a refactor to the error chain
// can't silently drop the 404 mapping (would surface as 500 to clients).
func TestHandlePay_OrderNotFound(t *testing.T) {
	t.Parallel()

	pay := &stubPaymentService{
		createIntentFn: func(_ context.Context, _ uuid.UUID) (domain.PaymentIntent, error) {
			return domain.PaymentIntent{}, domain.ErrOrderNotFound
		},
	}
	r := newRouterWith(&stubBookingService{}, &stubEventService{}, pay)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+uuid.New().String()+"/pay", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandlePay_NotAwaitingPayment: 409 when the order is in any
// status other than awaiting_payment. The service returns
// payment.ErrOrderNotAwaitingPayment; mapError translates to 409.
func TestHandlePay_NotAwaitingPayment(t *testing.T) {
	t.Parallel()

	pay := &stubPaymentService{
		createIntentFn: func(_ context.Context, _ uuid.UUID) (domain.PaymentIntent, error) {
			// Use the actual sentinel from the payment package — this
			// pins that handler_test imports the right symbol so a
			// rename cascades.
			return domain.PaymentIntent{}, paymentapp.ErrOrderNotAwaitingPayment
		},
	}
	r := newRouterWith(&stubBookingService{}, &stubEventService{}, pay)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+uuid.New().String()+"/pay", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	var got dto.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "order is not awaiting payment", got.Error)
}

// TestHandlePay_ReservationExpired: 409 when reservation TTL elapsed.
func TestHandlePay_ReservationExpired(t *testing.T) {
	t.Parallel()

	pay := &stubPaymentService{
		createIntentFn: func(_ context.Context, _ uuid.UUID) (domain.PaymentIntent, error) {
			return domain.PaymentIntent{}, paymentapp.ErrReservationExpired
		},
	}
	r := newRouterWith(&stubBookingService{}, &stubEventService{}, pay)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+uuid.New().String()+"/pay", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	var got dto.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "reservation expired", got.Error)
}
