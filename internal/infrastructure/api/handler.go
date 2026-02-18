package api

import (
	"encoding/json"
	"net/http"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

type BookingHandler interface {
	HandleBook(c *gin.Context)
	HandleListBookings(c *gin.Context)
	HandleCreateEvent(c *gin.Context)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		if err == domain.ErrSoldOut {
			statusCode = http.StatusConflict
			body = `{"error":"sold out"}`
		} else if err == domain.ErrUserAlreadyBought {
			statusCode = http.StatusConflict
			body = `{"error":"user already bought ticket"}`
		} else {
			// Use json.Marshal to safely encode the error message (handles quotes, backslashes, etc.)
			errJSON, _ := json.Marshal(gin.H{"error": err.Error()})
			statusCode = http.StatusInternalServerError
			body = string(errJSON)
		}
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
	var req createEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	event, err := h.eventService.CreateEvent(c.Request.Context(), req.Name, req.TotalTickets)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, event)
}

func RegisterRoutes(r *gin.RouterGroup, handler BookingHandler) {
	r.POST("/book", handler.HandleBook)
	r.GET("/history", handler.HandleListBookings)
	r.POST("/events", handler.HandleCreateEvent)
}
