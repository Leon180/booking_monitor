package booking

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type tracingBookingHandler struct {
	handler BookingHandler
}

func NewTracingBookingHandler(handler BookingHandler) BookingHandler {
	return &tracingBookingHandler{handler: handler}
}

// recordHTTPResult sets http.status_code and OTEL span status. Per OTEL
// semantic conventions, span.status should be Error for 5xx (server
// failure) and for the 4xx subset that represents a client error we
// still want visible in traces (e.g. 4xx rate dashboards). We mark
// 4xx as Error too, which matches the action-list N4 request, so any
// non-2xx/3xx response shows up prominently in Jaeger search.
func recordHTTPResult(span trace.Span, status int) {
	span.SetAttributes(attribute.Int("http.status_code", status))
	if status >= 400 {
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", status))
	}
}

func (h *tracingBookingHandler) HandleBook(c *gin.Context) {
	ctx := c.Request.Context()
	ctx, span := otel.Tracer("api").Start(ctx, "HandleBook")
	defer span.End()

	c.Request = c.Request.WithContext(ctx)

	h.handler.HandleBook(c)

	recordHTTPResult(span, c.Writer.Status())
}

func (h *tracingBookingHandler) HandleListBookings(c *gin.Context) {
	ctx := c.Request.Context()
	ctx, span := otel.Tracer("api").Start(ctx, "HandleListBookings")
	defer span.End()

	c.Request = c.Request.WithContext(ctx)

	h.handler.HandleListBookings(c)

	recordHTTPResult(span, c.Writer.Status())
}

func (h *tracingBookingHandler) HandleCreateEvent(c *gin.Context) {
	ctx := c.Request.Context()
	ctx, span := otel.Tracer("api").Start(ctx, "HandleCreateEvent")
	defer span.End()

	c.Request = c.Request.WithContext(ctx)

	h.handler.HandleCreateEvent(c)

	recordHTTPResult(span, c.Writer.Status())
}

func (h *tracingBookingHandler) HandleViewEvent(c *gin.Context) {
	ctx := c.Request.Context()
	ctx, span := otel.Tracer("api").Start(ctx, "HandleViewEvent")
	defer span.End()

	c.Request = c.Request.WithContext(ctx)

	h.handler.HandleViewEvent(c)

	recordHTTPResult(span, c.Writer.Status())
}
