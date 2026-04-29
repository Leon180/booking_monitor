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
	"net/http"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"
	"booking_monitor/internal/infrastructure/observability"
	"booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// orderSelfLink is the canonical poll URL for a freshly-accepted
// booking. Centralised so the handler, DTO, and tests can't drift
// from a hardcoded route literal in three places. Versioned with the
// rest of the API surface — bumping `/api/v1` to `/api/v2` only needs
// this prefix changed.
const orderSelfLinkPrefix = "/api/v1/orders/"

type BookingHandler interface {
	HandleBook(c *gin.Context)
	HandleGetOrder(c *gin.Context)
	HandleListBookings(c *gin.Context)
	HandleCreateEvent(c *gin.Context)
	HandleViewEvent(c *gin.Context)
}

type bookingHandler struct {
	service         application.BookingService
	eventService    application.EventService
	idempotencyRepo domain.IdempotencyRepository
}

func NewBookingHandler(service application.BookingService, eventService application.EventService, idempotencyRepo domain.IdempotencyRepository) BookingHandler {
	return &bookingHandler{service: service, eventService: eventService, idempotencyRepo: idempotencyRepo}
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

func (h *bookingHandler) HandleBook(c *gin.Context) {
	ctx := c.Request.Context()

	var req dto.BookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warn(ctx, "invalid book request body", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid request body"})
		return
	}

	// Idempotency key check
	idempotencyKey := c.GetHeader("Idempotency-Key")
	if len(idempotencyKey) > 128 {
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "Idempotency-Key must be 128 characters or fewer"})
		return
	}
	if idempotencyKey != "" {
		if cached, err := h.idempotencyRepo.Get(ctx, idempotencyKey); err == nil && cached != nil {
			c.Header("X-Idempotency-Replayed", "true")
			c.Data(cached.StatusCode, "application/json", []byte(cached.Body))
			return
		}
	}

	orderID, err := h.service.BookTicket(ctx, req.UserID, req.EventID, req.Quantity)

	var statusCode int
	var body string
	if err != nil {
		// Log the raw error with full context server-side, then translate to
		// a sanitized public message via mapError so we never leak DB errors.
		log.Error(ctx, "BookTicket failed",
			tag.Error(err),
			tag.UserID(req.UserID),
			tag.EventID(req.EventID),
			tag.Quantity(req.Quantity),
		)

		status, publicMsg := mapError(err)
		// Marshal via the DTO so the wire shape is centralised — every
		// error response the API emits goes through dto.ErrorResponse.
		// The marshal cannot fail for this fixed-shape struct.
		errJSON, _ := json.Marshal(dto.ErrorResponse{Error: publicMsg})
		statusCode = status
		body = string(errJSON)
	} else {
		// 202 Accepted — Redis-side deduct succeeded (the load-shed
		// gate); the rest of the lifecycle (DB persist, payment
		// charge, saga) is async. The client polls
		// `GET /api/v1/orders/:id` for the terminal status.
		statusCode = http.StatusAccepted
		acceptedJSON, _ := json.Marshal(dto.BookingAcceptedResponse{
			OrderID: orderID,
			Status:  "processing",
			Message: "booking accepted, awaiting confirmation",
			Links: dto.BookingLinks{
				Self: orderSelfLinkPrefix + orderID.String(),
			},
		})
		body = string(acceptedJSON)
	}

	// Cache the result for idempotency. The response was already
	// generated; a Set failure here just means the next retry
	// re-processes (still safe — same fingerprint, same
	// computation). Don't fail the in-flight request, but DO
	// surface the error so operators can correlate metric spikes
	// (`cache_idempotency_oversize_total`, Redis errors) with
	// specific keys at debug time.
	if idempotencyKey != "" {
		if setErr := h.idempotencyRepo.Set(ctx, idempotencyKey, &domain.IdempotencyResult{
			StatusCode: statusCode,
			Body:       body,
		}); setErr != nil {
			log.Warn(ctx, "idempotency cache Set failed (response already sent; next retry will re-process)",
				tag.Error(setErr),
				log.String("idempotency_key", idempotencyKey),
				log.Int("body_size_bytes", len(body)),
			)
		}
	}

	c.Data(statusCode, "application/json", []byte(body))
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
func RegisterRoutes(r *gin.RouterGroup, handler BookingHandler) {
	r.POST("/book", handler.HandleBook)
	r.GET("/orders/:id", handler.HandleGetOrder)
	r.GET("/history", handler.HandleListBookings)
	r.POST("/events", handler.HandleCreateEvent)
	r.GET("/events/:id", handler.HandleViewEvent)
}
