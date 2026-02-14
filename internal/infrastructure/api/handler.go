package api

import (
	"net/http"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
)

type BookingHandler struct {
	service *application.BookingService
}

func NewBookingHandler(service *application.BookingService) *BookingHandler {
	return &BookingHandler{service: service}
}

type bookRequest struct {
	UserID   int `json:"user_id" binding:"required"`
	EventID  int `json:"event_id" binding:"required"`
	Quantity int `json:"quantity" binding:"required,min=1,max=10"`
}

func (h *BookingHandler) HandleBook(c *gin.Context) {
	// Propagate context from Gin (which might include OTEL headers if middleware used)
	ctx := c.Request.Context()
	ctx, span := otel.Tracer("api/handler").Start(ctx, "HandleBook")
	defer span.End()

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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "booking successful"})
}

func RegisterRoutes(r *gin.RouterGroup, handler *BookingHandler) {
	r.POST("/book", handler.HandleBook)
}
