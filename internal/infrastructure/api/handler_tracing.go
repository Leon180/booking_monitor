package api

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type tracingBookingHandler struct {
	handler BookingHandler
}

func NewTracingBookingHandler(handler BookingHandler) BookingHandler {
	return &tracingBookingHandler{handler: handler}
}

func (h *tracingBookingHandler) HandleBook(c *gin.Context) {
	ctx := c.Request.Context()
	ctx, span := otel.Tracer("api").Start(ctx, "HandleBook")
	defer span.End()

	c.Request = c.Request.WithContext(ctx)

	h.handler.HandleBook(c)

	status := c.Writer.Status()
	span.SetAttributes(attribute.Int("http.status_code", status))
	if status >= 500 {
		span.SetStatus(codes.Error, "Internal Server Error")
	}
}

func (h *tracingBookingHandler) HandleListBookings(c *gin.Context) {
	ctx := c.Request.Context()
	ctx, span := otel.Tracer("api").Start(ctx, "HandleListBookings")
	defer span.End()

	c.Request = c.Request.WithContext(ctx)

	h.handler.HandleListBookings(c)

	status := c.Writer.Status()
	span.SetAttributes(attribute.Int("http.status_code", status))
	if status >= 500 {
		span.SetStatus(codes.Error, "Internal Server Error")
	}
}

func (h *tracingBookingHandler) HandleCreateEvent(c *gin.Context) {
	ctx := c.Request.Context()
	ctx, span := otel.Tracer("api").Start(ctx, "HandleCreateEvent")
	defer span.End()

	c.Request = c.Request.WithContext(ctx)

	h.handler.HandleCreateEvent(c)

	status := c.Writer.Status()
	span.SetAttributes(attribute.Int("http.status_code", status))
	if status >= 500 {
		span.SetStatus(codes.Error, "Internal Server Error")
	}
}
