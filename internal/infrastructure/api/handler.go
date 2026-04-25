package api

import (
	"encoding/json"
	"net/http"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"
	"booking_monitor/internal/infrastructure/observability"
	"booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"

	"github.com/gin-gonic/gin"
)

type BookingHandler interface {
	HandleBook(c *gin.Context)
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

	err := h.service.BookTicket(ctx, req.UserID, req.EventID, req.Quantity)

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
		statusCode = http.StatusOK
		successJSON, _ := json.Marshal(dto.BookingSuccessResponse{Message: "booking successful"})
		body = string(successJSON)
	}

	// Cache the result for idempotency
	if idempotencyKey != "" {
		_ = h.idempotencyRepo.Set(ctx, idempotencyKey, &domain.IdempotencyResult{
			StatusCode: statusCode,
			Body:       body,
		})
	}

	c.Data(statusCode, "application/json", []byte(body))
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

	c.JSON(http.StatusCreated, dto.EventResponseFromDomain(*event))
}

func (h *bookingHandler) HandleViewEvent(c *gin.Context) {
	// 記錄進入頁面的人數 (Conversion: Page Views)
	observability.PageViewsTotal.WithLabelValues("event_detail").Inc()
	c.JSON(http.StatusOK, gin.H{"message": "View event", "event_id": c.Param("id")})
}

func RegisterRoutes(r *gin.RouterGroup, handler BookingHandler) {
	r.POST("/book", handler.HandleBook)
	r.GET("/history", handler.HandleListBookings)
	r.POST("/events", handler.HandleCreateEvent)
	r.GET("/events/:id", handler.HandleViewEvent)
}
