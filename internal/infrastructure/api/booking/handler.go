// Package booking is the HTTP boundary for the customer-facing
// booking surface: POST /book, GET /history, POST /events,
// GET /events/:id. Lives under internal/infrastructure/api/booking
// so future operational endpoints (health, auth, admin) live in
// sibling packages without colliding with business endpoints.
//
// Public surface:
//   - BookingHandler   — interface implemented by the plain handler
//                        and the tracing decorator
//   - NewBookingHandler — fx-friendly constructor
//   - RegisterRoutes   — wires the handler onto a Gin RouterGroup
//
// The tracing decorator (handler_tracing.go) and error→HTTP translator
// (errors.go) live alongside in this package — they're pure
// implementation details of the booking boundary.
package booking

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	bookingapp "booking_monitor/internal/application/booking"
	"booking_monitor/internal/application/event"
	paymentapp "booking_monitor/internal/application/payment"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"
	"booking_monitor/internal/infrastructure/api/middleware"
	"booking_monitor/internal/infrastructure/observability"
	"booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// orderSelfLinkPrefix is the canonical poll URL for a freshly-accepted
// booking. Centralised so the handler, DTO, and tests can't drift
// from a hardcoded route literal in three places. Versioned with the
// rest of the API surface — bumping `/api/v1` to `/api/v2` only needs
// this prefix changed.
const orderSelfLinkPrefix = "/api/v1/orders/"

// orderPayLinkSuffix is appended to orderSelfLinkPrefix + orderID to
// produce the D4 payment endpoint
// (POST /api/v1/orders/:id/pay). D3 emits this URL in the booking
// response; D4 implements the handler. Until then, the route 404s —
// the link is forward-looking but the wire contract is set now so
// clients written against D3 don't need to be redeployed at D4.
const orderPayLinkSuffix = "/pay"

// mustMarshal panics on json.Marshal error. Reserved for fixed-shape
// DTO structs whose fields are all directly-marshalable types — the
// marshal cannot fail in practice, but the panic surfaces an
// impossible-condition violation loudly if a future field addition
// introduces a non-serialisable type (custom MarshalJSON that errors,
// pointer cycle, channel/func type). Without this guard, a `_` discard
// would commit an empty body to the HTTP response silently.
//
// Production wires `gin.Recovery()` middleware ahead of any handler
// (see cmd/booking-cli/server.go); a panic here surfaces as a 500.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("impossible: marshal %T: %v", v, err))
	}
	return b
}

type BookingHandler interface {
	HandleBook(c *gin.Context)
	HandleGetOrder(c *gin.Context)
	HandleListBookings(c *gin.Context)
	HandleCreateEvent(c *gin.Context)
	HandleViewEvent(c *gin.Context)
	HandleCreatePaymentIntent(c *gin.Context)
}

type bookingHandler struct {
	service        bookingapp.Service
	eventService   event.Service
	paymentService paymentapp.Service
}

// NewBookingHandler constructs the booking handler. The idempotency
// concern is handled by `middleware.Idempotency` (registered ahead of
// the handler in server.go), so the handler itself no longer
// depends on `domain.IdempotencyRepository` — its surface is purely
// business-logic dependencies.
//
// D4 adds paymentService for the /api/v1/orders/:id/pay endpoint.
// Eventually D5 will add a webhook handler — that one's wire is
// async / non-Service-shaped enough that it'll likely live in its
// own subpackage rather than extending booking. Hence we don't
// promote `api/payment/` yet.
func NewBookingHandler(service bookingapp.Service, eventService event.Service, paymentService paymentapp.Service) BookingHandler {
	return &bookingHandler{
		service:        service,
		eventService:   eventService,
		paymentService: paymentService,
	}
}

func (h *bookingHandler) HandleListBookings(c *gin.Context) {
	ctx := c.Request.Context()

	var params dto.ListBookingsQueryParams
	if err := c.ShouldBindQuery(&params); err != nil {
		log.Warn(ctx, "invalid history query params", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid query parameters"})
		return
	}

	if params.Page < 1 {
		params.Page = 1
	}
	if params.Size < 1 {
		params.Size = 10
	}
	// Ceiling on Size — prevents `?size=1000000` from triggering a full
	// table scan + multi-MB allocation in one request, which bypasses
	// nginx rate-limit (one request can do the damage). 100 chosen as a
	// generous upper bound for legitimate paginated UI use; raise via
	// configuration when a real consumer needs more. Closes Phase 2
	// checkpoint S1.
	const maxBookingHistoryPageSize = 100
	if params.Size > maxBookingHistoryPageSize {
		params.Size = maxBookingHistoryPageSize
	}

	orders, total, err := h.service.GetBookingHistory(ctx, params.Page, params.Size, params.StatusFilter())
	if err != nil {
		log.Error(ctx, "GetBookingHistory failed", tag.Error(err))
		status, public := mapError(err)
		c.JSON(status, dto.ErrorResponse{Error: public})
		return
	}

	c.JSON(http.StatusOK, dto.ListBookingsResponseFromDomain(orders, total, params.Page, params.Size))
}

// HandleBook is the booking-specific handler — pure business logic.
//
// Idempotency-Key validation, body fingerprinting, cache lookup,
// replay, 409-on-mismatch, and conditional cache-write are ALL handled
// by `middleware.Idempotency` registered ahead of this handler in
// `RegisterRoutes`. The handler is invoked exactly once per fresh
// request (cache replays short-circuit before reaching here) and
// concerns itself only with: parse body → call service → render
// response. The middleware captures the response body via
// gin.ResponseWriter wrapping so the cache-write decision happens
// after this handler returns, transparently.
//
// Note: even though body_size middleware wraps the raw body with
// MaxBytesReader and the idempotency middleware re-feeds the buffer
// via bytes.NewReader, ShouldBindJSON below works on the re-fed
// in-memory buffer — no double-read concerns.
func (h *bookingHandler) HandleBook(c *gin.Context) {
	ctx := c.Request.Context()

	var req dto.BookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warn(ctx, "invalid book request body", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid request body"})
		return
	}

	order, err := h.service.BookTicket(ctx, req.UserID, req.EventID, req.Quantity)
	if err != nil {
		// Log the raw error with full context server-side, then
		// translate to a sanitized public message via mapError so
		// we never leak DB errors.
		log.Error(ctx, "BookTicket failed",
			tag.Error(err),
			tag.UserID(req.UserID),
			tag.EventID(req.EventID),
			tag.Quantity(req.Quantity),
		)
		status, publicMsg := mapError(err)
		// Marshal via mustMarshal so an impossible non-marshalable
		// future field surfaces as a panic (caught by gin.Recovery)
		// rather than a silent empty body.
		c.Data(status, "application/json", mustMarshal(dto.ErrorResponse{Error: publicMsg}))
		return
	}

	// 202 Accepted — D3 (Pattern A): Redis-side reservation succeeded
	// (the load-shed gate). The worker is in flight to persist the
	// row as `awaiting_payment` with `reserved_until` set. The client
	// must call `Links.Pay` (POST /api/v1/orders/:id/pay) within
	// `expires_in_seconds` to actually charge — the legacy
	// auto-charge worker is bypassed for Pattern A. The `self` link
	// remains available for status polling throughout.
	expiresIn := int(time.Until(order.ReservedUntil()).Seconds())
	if expiresIn < 0 {
		// Defensive: a clock-skew or absurdly-late delivery could push
		// this negative. Floor at 0 so the wire field is never a
		// confusing negative duration; the absolute reserved_until
		// remains authoritative.
		expiresIn = 0
	}
	c.Data(http.StatusAccepted, "application/json", mustMarshal(dto.BookingAcceptedResponse{
		OrderID:          order.ID(),
		Status:           dto.BookingStatusReserved,
		Message:          "reservation accepted; complete payment before reserved_until",
		ReservedUntil:    order.ReservedUntil().UTC(),
		ExpiresInSeconds: expiresIn,
		Links: dto.BookingLinks{
			Self: orderSelfLinkPrefix + order.ID().String(),
			Pay:  orderSelfLinkPrefix + order.ID().String() + orderPayLinkSuffix,
		},
	}))
}

// HandleGetOrder is the polling endpoint for a freshly-accepted
// booking. Clients receive an `order_id` from `POST /book` and poll
// here for the terminal status (confirmed / failed / compensated).
//
// 404 contract: the worker persists the order row asynchronously (~ms
// after the 202 returns). Until then, GET returns 404 — clients
// should retry with a short backoff. After the row exists, every
// subsequent GET returns the latest status.
//
// No auth today — anyone with the order_id can read. N9 will add
// JWT + ownership check (the order belongs to its user_id). Documented
// as a known gap in PROJECT_SPEC §6.
func (h *bookingHandler) HandleGetOrder(c *gin.Context) {
	ctx := c.Request.Context()

	rawID := c.Param("id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		log.Warn(ctx, "invalid order id parameter",
			tag.Error(err),
			log.String("raw_id", rawID),
		)
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid order id"})
		return
	}

	order, err := h.service.GetOrder(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrOrderNotFound) {
			// Distinct log level from upstream errors — 404 is the
			// expected path during the brief post-202 window before
			// the worker persists.
			log.Debug(ctx, "order not found (may be in async-processing window)",
				tag.OrderID(id))
			c.JSON(http.StatusNotFound, dto.ErrorResponse{Error: "order not found"})
			return
		}
		log.Error(ctx, "GetOrder failed", tag.Error(err), tag.OrderID(id))
		status, public := mapError(err)
		c.JSON(status, dto.ErrorResponse{Error: public})
		return
	}

	c.JSON(http.StatusOK, dto.OrderResponseFromDomain(order))
}

func (h *bookingHandler) HandleCreateEvent(c *gin.Context) {
	ctx := c.Request.Context()

	var req dto.CreateEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warn(ctx, "invalid create event request", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid request body"})
		return
	}

	event, err := h.eventService.CreateEvent(ctx, req.Name, req.TotalTickets)
	if err != nil {
		log.Error(ctx, "CreateEvent failed",
			tag.Error(err),
			log.String("name", req.Name),
			log.Int("total_tickets", req.TotalTickets),
		)
		status, public := mapError(err)
		c.JSON(status, dto.ErrorResponse{Error: public})
		return
	}

	c.JSON(http.StatusCreated, dto.EventResponseFromDomain(event))
}

func (h *bookingHandler) HandleViewEvent(c *gin.Context) {
	// 記錄進入頁面的人數 (Conversion: Page Views)
	observability.PageViewsTotal.WithLabelValues("event_detail").Inc()
	c.JSON(http.StatusOK, gin.H{"message": "View event", "event_id": c.Param("id")})
}

// HandleCreatePaymentIntent is the Pattern A /pay (D4) entry point.
// Body is empty (the order_id comes from the path). The response
// carries the gateway-issued PaymentIntent's id + client_secret +
// amount + currency — the client uses the client_secret with Stripe
// Elements (or our mock equivalent) to actually move money.
//
// HTTP mapping:
//
//	200 OK                  intent created (or retrieved — idempotent)
//	400 Bad Request         malformed UUID in path
//	404 Not Found           order doesn't exist
//	409 Conflict            order isn't AwaitingPayment, or reservation expired
//	500 Internal Server     transient gateway / DB failure
//
// Why 200 (not 201): the gateway-side intent might be a replay (same
// orderID → same intent), so "Created" semantics aren't quite right.
// Stripe's own /v1/payment_intents returns 200 on retrieve and 201 on
// initial create; we collapse both to 200 because our mock doesn't
// distinguish (and clients shouldn't depend on the difference for
// idempotent retries).
//
// Idempotency middleware is NOT attached to this route. The gateway
// itself is idempotent on orderID (per the PaymentIntentCreator
// contract), so /pay is safe to retry without an application-side
// cache. Adding the middleware would just be ceremony.
func (h *bookingHandler) HandleCreatePaymentIntent(c *gin.Context) {
	ctx := c.Request.Context()

	idStr := c.Param("id")
	orderID, err := uuid.Parse(idStr)
	if err != nil {
		log.Warn(ctx, "invalid order_id in /pay path", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid order id"})
		return
	}

	intent, err := h.paymentService.CreatePaymentIntent(ctx, orderID)
	if err != nil {
		log.Error(ctx, "CreatePaymentIntent failed", tag.OrderID(orderID), tag.Error(err))
		status, publicMsg := mapError(err)
		c.Data(status, "application/json", mustMarshal(dto.ErrorResponse{Error: publicMsg}))
		return
	}

	c.Data(http.StatusOK, "application/json", mustMarshal(dto.PaymentIntentResponse{
		OrderID:         orderID,
		PaymentIntentID: intent.ID,
		ClientSecret:    intent.ClientSecret,
		AmountCents:     intent.AmountCents,
		Currency:        intent.Currency,
	}))
}

// RegisterRoutes wires the booking endpoints under a versioned router
// group (typically /api/v1). Operational endpoints (/livez, /readyz)
// live in the ops subpackage and register at the engine root, NOT
// under this group — k8s probe targets must not move with API
// versioning.
//
// The idempotency middleware is attached PER-ROUTE rather than to
// the whole group: only state-changing endpoints (POST /book today;
// future POST /cancel, POST /refund, etc.) need it. Idempotent reads
// (GET /history, GET /events/:id, GET /orders/:id) and event setup
// (POST /events — admin-side, low collision risk) skip the cache
// lookup entirely. /pay also skips because the gateway is idempotent
// on orderID — see HandleCreatePaymentIntent doc for the rationale.
func RegisterRoutes(r *gin.RouterGroup, handler BookingHandler, idempotencyRepo domain.IdempotencyRepository) {
	r.POST("/book", middleware.Idempotency(idempotencyRepo), handler.HandleBook)
	r.GET("/orders/:id", handler.HandleGetOrder)
	r.POST("/orders/:id/pay", handler.HandleCreatePaymentIntent)
	r.GET("/history", handler.HandleListBookings)
	r.POST("/events", handler.HandleCreateEvent)
	r.GET("/events/:id", handler.HandleViewEvent)
}
