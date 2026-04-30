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

	"booking_monitor/internal/application"
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
}

type bookingHandler struct {
	service      application.BookingService
	eventService application.EventService
}

// NewBookingHandler constructs the booking handler. The idempotency
// concern is handled by `middleware.Idempotency` (registered ahead of
// the handler in server.go), so the handler itself no longer
// depends on `domain.IdempotencyRepository` — its surface is purely
// business-logic dependencies.
func NewBookingHandler(service application.BookingService, eventService application.EventService) BookingHandler {
	return &bookingHandler{service: service, eventService: eventService}
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

	orderID, err := h.service.BookTicket(ctx, req.UserID, req.EventID, req.Quantity)
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

	// 202 Accepted — Redis-side deduct succeeded (the load-shed
	// gate); the rest of the lifecycle (DB persist, payment charge,
	// saga) is async. The client polls `GET /api/v1/orders/:id` for
	// the terminal status.
	c.Data(http.StatusAccepted, "application/json", mustMarshal(dto.BookingAcceptedResponse{
		OrderID: orderID,
		Status:  dto.BookingStatusProcessing,
		Message: "booking accepted, awaiting confirmation",
		Links: dto.BookingLinks{
			Self: orderSelfLinkPrefix + orderID.String(),
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
// lookup entirely.
func RegisterRoutes(r *gin.RouterGroup, handler BookingHandler, idempotencyRepo domain.IdempotencyRepository) {
	r.POST("/book", middleware.Idempotency(idempotencyRepo), handler.HandleBook)
	r.GET("/orders/:id", handler.HandleGetOrder)
	r.GET("/history", handler.HandleListBookings)
	r.POST("/events", handler.HandleCreateEvent)
	r.GET("/events/:id", handler.HandleViewEvent)
}
