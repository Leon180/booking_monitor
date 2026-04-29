package middleware_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/infrastructure/api/middleware"
)

func init() { gin.SetMode(gin.TestMode) }

// TestBodySize covers the three rejection paths so a future refactor
// can't silently drop one:
//   - Content-Length advertised > cap → fast-fail before handler
//   - Chunked / no Content-Length, but body > cap → MaxBytesReader
//     surfaces as 413 once the handler tries to read
//   - Within cap → handler runs and request body is fully readable
//
// The cap distinction matters because a misimplemented middleware is
// easy to write — e.g. only checking ContentLength and missing chunked
// uploads, or only catching the read overflow and letting a 5GB
// Content-Length advertisement waste a connection slot.
func TestBodySize(t *testing.T) {
	t.Parallel()

	const maxBytes int64 = 64

	cases := []struct {
		name         string
		buildRequest func() *http.Request
		wantStatus   int
		wantHandler  bool
	}{
		{
			name: "within_cap_passes_through",
			buildRequest: func() *http.Request {
				body := []byte(`{"user_id":1,"event_id":"x","quantity":1}`)
				req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
				req.ContentLength = int64(len(body))
				req.Header.Set("Content-Type", "application/json")
				return req
			},
			wantStatus:  http.StatusOK,
			wantHandler: true,
		},
		{
			name: "advertised_content_length_over_cap_rejected_fast",
			buildRequest: func() *http.Request {
				// Body itself fits, but ContentLength lies — middleware
				// must reject on the advertisement, not bother with the
				// body. Simulates an attacker sending Content-Length
				// header ahead of (slow-loris-style) the body bytes.
				req := httptest.NewRequest(http.MethodPost, "/test",
					bytes.NewReader([]byte(`{"x":1}`)))
				req.ContentLength = maxBytes + 1
				return req
			},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantHandler: false,
		},
		{
			name: "chunked_body_over_cap_caught_by_max_bytes_reader",
			buildRequest: func() *http.Request {
				// No Content-Length — simulates chunked Transfer-Encoding
				// (which has no advance size header). MaxBytesReader
				// catches this when the handler reads.
				body := strings.Repeat("a", int(maxBytes)+10)
				req := httptest.NewRequest(http.MethodPost, "/test",
					bytes.NewReader([]byte(body)))
				req.ContentLength = -1 // unknown
				return req
			},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantHandler: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := gin.New()
			r.Use(middleware.BodySize(maxBytes))

			handlerCalled := false
			r.POST("/test", func(c *gin.Context) {
				// Drain body so MaxBytesReader gets a chance to fail
				// for the chunked case. We deliberately do NOT call
				// c.AbortWithError on read failure — that would write
				// 400 and shadow the middleware's post-handler 413
				// hook. Realistic handlers behave this way too: they
				// stash the bind error in c.Errors and let middleware
				// translate to the right HTTP code.
				body, err := c.GetRawData()
				if err != nil {
					c.Error(err) //nolint:errcheck // recorded in c.Errors
					return
				}
				_ = body
				handlerCalled = true
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			r.ServeHTTP(w, tc.buildRequest())

			assert.Equal(t, tc.wantStatus, w.Code, "status code")
			assert.Equal(t, tc.wantHandler, handlerCalled, "handler invocation")

			if tc.wantStatus == http.StatusRequestEntityTooLarge {
				require.Contains(t, w.Body.String(), "request body exceeds size limit",
					"413 response must use the canonical dto.ErrorResponse message")
			}
		})
	}
}
