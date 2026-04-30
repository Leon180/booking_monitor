// Package middleware hosts cross-cutting Gin middleware. Idempotency
// lives here (not inside the booking handler package) because the
// contract is endpoint-agnostic: any future state-changing endpoint
// (`/cancel`, `/refund`, admin mutations) can opt in by adding the
// middleware to its route group, with no business-handler changes.
//
// Pre-N4-refactor the entire idempotency lifecycle was inlined inside
// `internal/infrastructure/api/booking/handler.go::HandleBook` —
// header validation, body fingerprinting, cache lookup, replay-or-
// 409 routing, and conditional cache-write all in ~150 lines.
// Lifting it to middleware:
//
//   - Handler shrinks back to business logic (~30 lines for booking)
//   - Adding a second idempotent endpoint becomes a one-line route
//     registration, not a copy-paste of the cache flow
//   - The contract evolves in a single file (this one)
//
// Industry pattern: AWS Lambda Powertools `@idempotent` decorator,
// Stripe's internal middleware-based approach, Shopify's GraphQL
// extension. Idempotency-as-cross-cutting is the canonical shape
// for production APIs.
package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"
	"booking_monitor/internal/infrastructure/observability"
	"booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// MaxIdempotencyKeyLen caps the Idempotency-Key header. Stripe
// documents 255-byte keys; we tighten to 128 because realistic
// UUIDv4/v7 keys are 36 chars and 128 still accommodates client-
// prefixed keys ("user-42:order-abc") with generous headroom while
// bounding pathological-key memory cost.
const MaxIdempotencyKeyLen = 128

// idempotencyKeyHeader is the canonical header name. Centralised so
// the middleware and any test using the contract don't drift on
// header-name capitalisation (Gin's GetHeader is canonicalising via
// textproto.CanonicalMIMEHeaderKey, but the literal source-of-truth
// belongs here).
const idempotencyKeyHeader = "Idempotency-Key"

// replayedHeader is the response header set on every idempotent
// replay. Clients distinguish "this is a replay of a prior request"
// from "this was processed fresh" by reading this header.
const replayedHeader = "X-Idempotency-Replayed"

// IsValidIdempotencyKey enforces ASCII-printable (0x20-0x7E) over the
// full key. Stripe restricts keys to this range; without the check,
// a malicious client could embed control characters that confuse log
// parsers, terminal output, or downstream systems that don't quote
// the key value. A key like "valid\x00prefix\nattack" passed Pre-N4
// length validation and got stored verbatim into Redis where the
// embedded \x00 / \n fragment downstream log consumers.
//
// Empty string returns true so the caller can decide whether absence
// is acceptable (this middleware: yes — header absence means
// idempotency is opt-out for the request).
func IsValidIdempotencyKey(key string) bool {
	if len(key) > MaxIdempotencyKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		b := key[i]
		if b < 0x20 || b > 0x7E {
			return false
		}
	}
	return true
}

// Fingerprint hashes the raw request body bytes via SHA-256 and
// returns the hex-encoded digest. We deliberately do NOT canonicalize
// JSON — the de facto industry contract (Stripe / Shopify / GitHub
// Octokit / AWS Powertools) is "client must send byte-identical
// retries". Canonicalisation adds CPU + ambiguity (whose canonical
// form?) for negligible benefit since the originating client
// controls its own marshaling.
//
// `sha256.Sum256(nil)` is well-defined and non-empty; same-user
// empty-body retries collide harmlessly because the key is part of
// the lookup. A fingerprint match without a key match is impossible.
func Fingerprint(bodyBytes []byte) string {
	sum := sha256.Sum256(bodyBytes)
	return hex.EncodeToString(sum[:])
}

// shouldCacheStatus is the cache-write policy. Only 2xx responses
// are cached.
//
// Why NOT 5xx (deviation from Stripe's documented behaviour):
//
//   - Stripe caches 5xx to prevent clients from retry-storming a
//     known-degraded payment gateway. That assumes the 5xx represents
//     stable degraded state.
//   - Our 5xx mostly reflect TRANSIENT or PROGRAMMER-ERROR conditions:
//     Redis Lua failures (Redis blip), unmapped error types in
//     `mapError`, etc. Pinning these for 24h is worse customer
//     experience than letting clients retry against a recovered
//     server.
//   - We have nginx rate-limiting at the edge — the retry-storm
//     concern Stripe optimised for is already mitigated.
//
// Why NOT 4xx (consistent with Stripe):
//
//   - 4xx validation errors burn the key for 24h on a typo'd body —
//     the client cannot correct + retry without generating a fresh key.
//   - 4xx business errors (sold-out 409, duplicate 409) reflect
//     transient business state that may resolve before the 24h TTL.
//
// 2xx is the only case where the response represents a stable,
// reproducible terminal outcome safe to replay.
func shouldCacheStatus(status int) bool {
	return status >= 200 && status < 300
}

// captureWriter wraps gin.ResponseWriter to copy the response body
// into an in-memory buffer alongside writing it to the client. This
// is the canonical Gin pattern for middleware that needs to inspect
// or store the response after the handler runs (the handler always
// writes via c.Writer, never returning bytes directly).
//
// Gotcha: must override BOTH Write and WriteString — otherwise calls
// like c.String(...) bypass the buffer. c.JSON / c.Data go through
// Write, c.String goes through WriteString.
type captureWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.body.Write(p)
	return w.ResponseWriter.Write(p)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// Idempotency wraps a route with the Stripe-style idempotency
// contract. Behaviour is opt-in per route (handlers without this
// middleware see no caching) and opt-in per request (the middleware
// is a no-op when the request lacks an Idempotency-Key header).
//
// Flow:
//
//   1. Read Idempotency-Key header. Absent → pass through, no caching.
//   2. Validate key (length + ASCII-printable). Invalid → 400.
//   3. Read body bytes ONCE (via c.GetRawData). Re-feed the body so
//      downstream handlers can ShouldBindJSON. The body-size
//      middleware MUST run before this so MaxBytesReader is in place.
//   4. Compute fingerprint = SHA-256 hex of raw body.
//   5. Cache GET:
//        - hit + matching fingerprint → replay, set X-Idempotency-Replayed
//        - hit + different fingerprint → 409 Conflict (Stripe contract)
//        - hit + empty fingerprint → legacy entry → replay + lazy
//          write-back the new fingerprint
//        - miss → call inner handler
//        - error → fail-open (call inner handler) AND skip post-processing
//          Set so a flaky-then-recovered Redis doesn't pin transient state
//   6. After inner handler runs: if response status is cacheable
//      (shouldCacheStatus) AND the cache GET didn't error this request,
//      Set the response.
func Idempotency(repo domain.IdempotencyRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Step 1: header presence
		key := c.GetHeader(idempotencyKeyHeader)
		if key == "" {
			c.Next()
			return
		}

		// Step 2: header validity
		if !IsValidIdempotencyKey(key) {
			c.AbortWithStatusJSON(http.StatusBadRequest, dto.ErrorResponse{
				Error: "Idempotency-Key must be ASCII-printable and at most 128 characters",
			})
			return
		}

		// Step 3: read body bytes once. Downstream handler will
		// ShouldBindJSON the same buffer (re-fed below).
		bodyBytes, err := c.GetRawData()
		if err != nil {
			var mbErr *http.MaxBytesError
			if errors.As(err, &mbErr) {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, dto.ErrorResponse{
					Error: "request body exceeds size limit",
				})
				return
			}
			log.Warn(ctx, "failed to read request body in idempotency middleware", tag.Error(err))
			c.AbortWithStatusJSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid request body"})
			return
		}

		// Step 4: fingerprint
		fp := Fingerprint(bodyBytes)

		// Step 5: cache lookup + routing
		cached, cachedFP, getErr := repo.Get(ctx, key)
		cacheGetFailed := getErr != nil

		switch {
		case getErr == nil && cached != nil:
			switch cachedFP {
			case "":
				// Legacy entry (pre-N4): no fingerprint stored. Replay
				// AND lazily write-back the freshly-computed fingerprint
				// so subsequent replays validate. Per-key migration
				// window closes on the FIRST replay; worst case is 24h
				// TTL for keys that never see a replay.
				observability.IdempotencyReplaysTotal.WithLabelValues("legacy_match").Inc()
				log.Info(ctx, "idempotency legacy entry replayed; writing back fingerprint",
					log.String("idempotency_key", key))
				if setErr := repo.Set(ctx, key, cached, fp); setErr != nil {
					log.Warn(ctx, "lazy fingerprint write-back failed; entry will retry on next replay",
						tag.Error(setErr),
						log.String("idempotency_key", key),
						log.String("idempotency_op", "lazy_writeback"))
				}
				replayCached(c, cached)
				return
			case fp:
				// Match — replay verbatim, do NOT call the handler.
				observability.IdempotencyReplaysTotal.WithLabelValues("match").Inc()
				replayCached(c, cached)
				return
			default:
				// Mismatch — Stripe's contract returns 409. The cached
				// body is NOT returned (would mislead the client into
				// thinking their new request succeeded); structured
				// error tells them to use a fresh key for the new body.
				// Both fingerprints are SHA-256 hashes (no PII) — log
				// them so an operator investigating "I sent the same
				// body and got 409!" can correlate via grep.
				observability.IdempotencyReplaysTotal.WithLabelValues("mismatch").Inc()
				log.Warn(ctx, "idempotency-key reused with a different request body",
					log.String("idempotency_key", key),
					log.String("cached_fingerprint", cachedFP),
					log.String("incoming_fingerprint", fp))
				c.AbortWithStatusJSON(http.StatusConflict, dto.ErrorResponse{
					Error: "Idempotency-Key reused with a different request body",
				})
				return
			}
		case getErr != nil:
			// Fail-open: log ERROR (sustained > 0 means idempotency is
			// suspended; companion `idempotency_cache_get_errors_total`
			// counter on the instrumented decorator is the page-worthy
			// metric). Continue to handler so the booking endpoint
			// stays available during a Redis outage.
			log.Error(ctx, "idempotency cache Get failed; processing as cache-miss (idempotency briefly suspended)",
				tag.Error(getErr),
				log.String("idempotency_key", key))
		}

		// Re-feed the body so the downstream handler can ShouldBindJSON.
		// MaxBytesReader (from BodySize middleware) has already bounded
		// the bytes; the inner read iterates an in-memory buffer.
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		// Wrap the response writer so we can capture the final body +
		// status after the handler runs.
		capture := &captureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = capture

		c.Next()

		// Step 6: post-handler cache write decision.
		//
		// Skip Set when:
		//   - The handler aborted before writing (status 0 / no body).
		//   - The status is not cacheable per shouldCacheStatus.
		//   - The cache GET errored on this request — caching a
		//     fresh response written through a flaky-then-recovered
		//     Redis would pin a possibly-transient state for 24h.
		statusCode := capture.Status()
		if !shouldCacheStatus(statusCode) || cacheGetFailed {
			return
		}

		// Use a detached background context for the Set: the request
		// context will be cancelled the moment the response is fully
		// flushed to the client. We want the cache write to complete
		// even if the client disconnects; otherwise replay protection
		// races client behaviour. The Set is best-effort regardless
		// (response is already committed), so a Redis blip here is
		// logged but does not surface to the user.
		setCtx := context.WithoutCancel(ctx)
		if setErr := repo.Set(setCtx, key, &domain.IdempotencyResult{
			StatusCode: statusCode,
			Body:       capture.body.String(),
		}, fp); setErr != nil {
			log.Warn(ctx, "idempotency cache Set failed (response already sent; next retry will re-process)",
				tag.Error(setErr),
				log.String("idempotency_key", key),
				log.String("idempotency_op", "final_set"),
				log.Int("body_size_bytes", capture.body.Len()))
		}
	}
}

// replayCached writes the cached response back to the client verbatim
// with the X-Idempotency-Replayed: true header so clients can
// distinguish replays from fresh processing. AbortWithStatusJSON
// would clobber the cached body shape; using c.Data preserves the
// exact bytes that were originally returned.
func replayCached(c *gin.Context, cached *domain.IdempotencyResult) {
	c.Header(replayedHeader, "true")
	c.Data(cached.StatusCode, "application/json", []byte(cached.Body))
	c.Abort()
}
