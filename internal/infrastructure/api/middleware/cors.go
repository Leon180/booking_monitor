package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS returns a gin middleware that handles cross-origin requests
// for the D8-minimal browser demo. The allow-list is exact-match
// against the request's `Origin` header; an empty allow-list disables
// the middleware entirely (production-safe default — a `git pull` on
// a production stack doesn't accidentally widen the surface).
//
// What the middleware emits on every response (NOT just preflight):
//
//	Access-Control-Allow-Origin: <echoed Origin if in allow-list>
//	Vary: Origin
//
// `Vary: Origin` matters because intermediate proxies / browser caches
// keyed only on URL would otherwise reuse one origin's response for
// another origin. Even for a local demo this is cheap correctness
// (Codex round-2 P2 finding).
//
// On preflight (OPTIONS) requests where the `Origin` matches:
//
//	Access-Control-Allow-Methods: GET, POST, OPTIONS
//	Access-Control-Allow-Headers: Content-Type, Idempotency-Key, X-Correlation-ID
//	Access-Control-Max-Age: 600
//	Vary: Access-Control-Request-Method, Access-Control-Request-Headers
//
// ACRM / ACRH are added to `Vary` only on preflight — they're the
// preflight cache-key axes. Adding them to non-preflight responses
// would emit noise (RFC 7231 §7.1.4 — Vary should list only headers
// that actually affected the response).
//
// Then short-circuits with 204 — no body, no further middleware.
// This MUST be mounted BEFORE the `Combined` middleware so OPTIONS
// preflights don't allocate correlation-ids or log noise.
//
// The allow-headers list is what the demo's actual requests need:
//   - `Content-Type` for `application/json` POST bodies (makes every
//     POST non-simple, triggering preflight)
//   - `Idempotency-Key` for `POST /api/v1/book` (Stripe-shape
//     deduplication header that the booking handler reads)
//   - `X-Correlation-ID` so a future demo-side trace surface can
//     thread through (currently unused but cheap to allow)
//
// Header-name comparison is case-insensitive per RFC 7230 §3.2,
// which `gin.Context.Request.Header` already normalises via Go's
// `http.Header.Get`. Tests assert this end-to-end.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o != "" {
			allowSet[o] = struct{}{}
		}
	}
	enabled := len(allowSet) > 0

	return func(c *gin.Context) {
		if !enabled {
			c.Next()
			return
		}

		// Vary: Origin fires on EVERY response (matched, unmatched,
		// OPTIONS, regular GET, no-Origin). A response that's missing
		// this header AND echoes Allow-Origin is cache-poisonable
		// across origins by an intermediate proxy. Setting it
		// unconditionally costs one header per response and avoids
		// the trap. ACRM / ACRH are scoped to OPTIONS below — they
		// only affect the preflight cache key, so emitting them on
		// non-preflight responses adds noise without affecting cache
		// correctness (RFC 7231 §7.1.4 — Vary should list only
		// headers that actually affected the response).
		c.Writer.Header().Add("Vary", "Origin")

		origin := c.GetHeader("Origin")
		if origin == "" {
			// Same-origin or non-browser caller — no CORS handling
			// needed beyond Vary. Continue normally.
			c.Next()
			return
		}
		if _, ok := allowSet[origin]; !ok {
			// Origin not in allow-list: do NOT echo Allow-Origin (the
			// browser will then reject the response per CORS spec).
			// Don't 403 — that would leak the allow-list shape to a
			// scanner; let the browser do its own rejection.
			c.Next()
			return
		}

		// Origin matches — echo it. Single value (not `*`) so the
		// browser accepts it with or without credentials.
		c.Writer.Header().Set("Access-Control-Allow-Origin", origin)

		if c.Request.Method == http.MethodOptions {
			// Preflight: ACRM + ACRH negotiate which method/headers
			// are allowed. Vary list these too so a proxy that caches
			// the 204 doesn't reuse it across requests with different
			// method-or-headers asks.
			c.Writer.Header().Add("Vary", "Access-Control-Request-Method")
			c.Writer.Header().Add("Vary", "Access-Control-Request-Headers")
			c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-Correlation-ID")
			c.Writer.Header().Set("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
