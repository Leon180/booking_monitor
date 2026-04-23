package api

import (
	"encoding/json"
	"net/http"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/observability"
	"booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
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

type bookRequest struct {
	UserID   int `json:"user_id" binding:"required"`
	EventID  int `json:"event_id" binding:"required"`
	Quantity int `json:"quantity" binding:"required,min=1,max=10"`
}

type listBookingsResponse struct {
	Data []*domain.Order `json:"data"`
	Meta meta            `json:"meta"`
}

type meta struct {
	Total int `json:"total"`
	Page  int `json:"page"`
	Size  int `json:"size"`
}

func (h *bookingHandler) HandleListBookings(c *gin.Context) {
	ctx := c.Request.Context()

	var req struct {
		Page   int     `form:"page"`
		Size   int     `form:"size"`
		Status *string `form:"status"`
	}
	if err := c.ShouldBindQuery(&req); err != nil {
		log.Warn(ctx, "invalid history query params", tag.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query parameters"})
		return
	}

	if req.Page < 1 {
		req.Page = 1
	}
	if req.Size < 1 {
		req.Size = 10
	}

	var orserStatus *domain.OrderStatus
	if req.Status != nil {
		orserStatus = lo.ToPtr(domain.OrderStatus(*req.Status))
	}

	orders, total, err := h.service.GetBookingHistory(ctx, req.Page, req.Size, orserStatus)
	if err != nil {
		log.Error(ctx, "GetBookingHistory failed", tag.Error(err))
		status, public := mapError(err)
		c.JSON(status, gin.H{"error": public})
		return
	}

	c.JSON(http.StatusOK, listBookingsResponse{
		Data: orders,
		Meta: meta{
			Total: total,
			Page:  req.Page,
			Size:  req.Size,
		},
	})
}

func (h *bookingHandler) HandleBook(c *gin.Context) {
	ctx := c.Request.Context()

	var req bookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warn(ctx, "invalid book request body", tag.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// Idempotency key check
	idempotencyKey := c.GetHeader("Idempotency-Key")
	if len(idempotencyKey) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Idempotency-Key must be 128 characters or fewer"})
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
		errJSON, _ := json.Marshal(gin.H{"error": publicMsg})
		statusCode = status
		body = string(errJSON)
	} else {
		statusCode = http.StatusOK
		body = `{"message":"booking successful"}`
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

type createEventRequest struct {
	Name         string `json:"name" binding:"required"`
	TotalTickets int    `json:"total_tickets" binding:"required,min=1"`
}

func (h *bookingHandler) HandleCreateEvent(c *gin.Context) {
	ctx := c.Request.Context()

	var req createEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warn(ctx, "invalid create event request", tag.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
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
		c.JSON(status, gin.H{"error": public})
		return
	}

	c.JSON(http.StatusCreated, event)
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
