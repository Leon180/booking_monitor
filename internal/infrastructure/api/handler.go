package api

import (
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
	service      application.BookingService
	eventService application.EventService
}

func NewBookingHandler(service application.BookingService, eventService application.EventService) BookingHandler {
	return &bookingHandler{service: service, eventService: eventService}
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
	// Propagate context from Gin (which might include OTEL headers if middleware used)
	ctx := c.Request.Context()

	var req bookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := h.service.BookTicket(ctx, req.UserID, req.EventID, req.Quantity)
	if err != nil {
		if err == domain.ErrSoldOut {
			c.JSON(http.StatusConflict, gin.H{"error": "sold out"})
			return
		}
		if err == domain.ErrUserAlreadyBought {
			c.JSON(http.StatusConflict, gin.H{"error": "user already bought ticket"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "booking successful"})
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
