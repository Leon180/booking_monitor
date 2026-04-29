package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"booking_monitor/internal/infrastructure/api/dto"
	"booking_monitor/internal/log"
)

// MaxBookingBodyBytes caps the request body for `POST /api/v1/book`
// (and any sibling write endpoint). 16KB is generous: a realistic
// booking JSON is ~80 bytes (`{"user_id":..,"event_id":"<uuid>","quantity":1}`);
// 16KB leaves room for future fields without enabling slow-loris-by-body.
//
// Why this number, not Stripe's 1MB or AWS's 10MB:
//   - The endpoints are NOT general-purpose data ingestion — they take
//     a tiny fixed-shape JSON. There's no legitimate caller producing
//     10KB, let alone 1MB.
//   - Caps belong tight. A 1MB cap means an attacker can amplify a
//     POST 50K× over the legitimate request size; a 16KB cap means
//     ~200×. Neither is great in isolation, but tighter is strictly
//     better.
//   - 16KB is also the typical TLS record size, so a single record
//     handles the entire request without buffer-grow round trips.
//
// Future: if we add admin endpoints that accept config blobs, those
// get their own larger cap via a separate middleware on the admin
// route group.
const MaxBookingBodyBytes int64 = 16 * 1024

// BodySize returns a Gin middleware that wraps the request body with
// http.MaxBytesReader. This is the canonical Go pattern for HTTP
// body size enforcement — it runs at the wire-read layer, before the
// JSON decoder ever sees a byte.
//
// Industry pattern (Stripe / Shopify / GitHub Octokit / AWS API
// Gateway): size validation lives at the HTTP boundary, NOT inside
// the storage layer. By the time a request reaches the cache /
// repository, the bytes are already bounded.
//
// On overflow: the next read on c.Request.Body returns
// `*http.MaxBytesError`. Most JSON-binding code (`c.ShouldBindJSON`)
// surfaces this as a generic error, so we pre-emptively detect the
// overflow at middleware time — the request gets a 413 Payload Too
// Large response and the handler is never called.
//
// Why preemptive detection (vs letting the binding error surface
// later as 400 Bad Request):
//   - 413 vs 400 is a real semantic distinction. A client retrying
//     a 400 may send the exact same payload again; a client seeing
//     413 knows the payload itself is the problem.
//   - The handler may have side effects between body-read and
//     binding (e.g., logging headers). Catching at middleware
//     prevents partial work for an eventual-413 request.
//
// Returns 413 with the dto.ErrorResponse JSON shape so the client
// sees a structured error consistent with other 4xx responses.
func BodySize(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Wrap the body. ANY subsequent read past maxBytes will fail.
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)

		// Pre-emptive detection: if Content-Length is set and exceeds
		// the cap, fail fast without consuming the body. Saves bandwidth
		// + handler invocation.
		//
		// (Content-Length is advisory — chunked-encoded requests have
		// no header. For those, MaxBytesReader catches the overflow at
		// read time. Belt-and-suspenders.)
		if c.Request.ContentLength > maxBytes {
			rejectOversize(c, c.Request.ContentLength, maxBytes)
			return
		}

		c.Next()

		// Post-handler check: if a downstream binder hit MaxBytesError
		// and recorded it in c.Errors, surface 413 if we haven't
		// already written a response. (Most request-validation paths
		// short-circuit on bind error and write their own 400, which
		// is wrong for the body-too-large case but acceptable —
		// surfacing here is best-effort.)
		for _, ginErr := range c.Errors {
			var maxBytesErr *http.MaxBytesError
			if errors.As(ginErr.Err, &maxBytesErr) && !c.Writer.Written() {
				rejectOversize(c, maxBytesErr.Limit, maxBytes)
				return
			}
		}
	}
}

// rejectOversize emits the canonical 413 response. Logs at Warn so
// operators see deliberate rejections without paging — sustained
// rate would suggest either a misbehaving client or an attack;
// each individual rejection is benign.
func rejectOversize(c *gin.Context, observed, maxBytes int64) {
	log.Warn(c.Request.Context(), "request body exceeds size cap",
		log.Int64("observed_bytes", observed),
		log.Int64("max_bytes", maxBytes),
		log.String("path", c.Request.URL.Path),
	)
	c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, dto.ErrorResponse{
		Error: "request body exceeds size limit",
	})
}
