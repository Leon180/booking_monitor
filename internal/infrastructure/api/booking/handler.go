// Package booking is the HTTP boundary for the customer-facing
// booking surface: POST /book, GET /history, POST /events,
// GET /events/:id. Lives under internal/infrastructure/api/booking
// so future operational endpoints (health, auth, admin) live in
// sibling packages without colliding with business endpoints.
//
// Public surface:
//   - BookingHandler   — interface implemented by the plain handler
//                        and the tracing decorator
//   - NewBookingHandler — fx-friendly constructor
//   - RegisterRoutes   — wires the handler onto a Gin RouterGroup
//
// The tracing decorator (handler_tracing.go) and error→HTTP translator
// (errors.go) live alongside in this package — they're pure
// implementation details of the booking boundary.
package booking

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"
	"booking_monitor/internal/infrastructure/observability"
	"booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// orderSelfLink is the canonical poll URL for a freshly-accepted
// booking. Centralised so the handler, DTO, and tests can't drift
// from a hardcoded route literal in three places. Versioned with the
// rest of the API surface — bumping `/api/v1` to `/api/v2` only needs
// this prefix changed.
const orderSelfLinkPrefix = "/api/v1/orders/"

// maxIdempotencyKeyLen caps the Idempotency-Key header per Stripe's
// documented 255-byte limit, but we tighten to 128 because our
// realistic UUIDv4/v7 keys are 36 chars; a 128-char ceiling still
// accommodates client-prefixed keys ("user-42:order-abc") with
// generous headroom while bounding pathological-key memory cost.
const maxIdempotencyKeyLen = 128

// isValidIdempotencyKey enforces ASCII-printable (0x20-0x7E) over the
// full key. Stripe restricts keys to this range; without the check,
// a malicious client could embed control characters that confuse
// log parsers, terminal output, or downstream systems that don't
// quote the key value.
//
// Pre-N4 only the length was checked — a key like
// "valid\x00prefix\nattack" passed validation and got stored verbatim
// into Redis under the prefix `idempotency:valid\x00prefix\nattack`,
// where the embedded \x00 / \n terminate or fragment downstream
// consumers (log aggregators, debug dumps).
func isValidIdempotencyKey(key string) bool {
	if len(key) > maxIdempotencyKeyLen {
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

// fingerprint hashes the raw request body bytes via SHA-256 and
// returns the hex-encoded digest. We deliberately do NOT canonicalize
// JSON (RFC 8785 / sort-keys) — the de facto industry contract
// (Stripe, Shopify, GitHub Octokit) is "client must send byte-
// identical retries". Canonicalization adds CPU + ambiguity (whose
// canonical form?) for negligible benefit since the original client
// controls its own marshaling.
//
// Empty body produces a deterministic hash (sha256 of zero bytes is
// well-defined); collision risk between a same-user empty-body
// retry and a different request is bounded by the fact that the key
// is also part of the lookup — a fingerprint match without a key
// match is impossible.
func fingerprint(bodyBytes []byte) string {
	sum := sha256.Sum256(bodyBytes)
	return hex.EncodeToString(sum[:])
}

// replayCached writes the cached idempotency response back to the
// client verbatim, with the X-Idempotency-Replayed header so clients
// can distinguish "this is a replay" from "this was processed". The
// header value is constant — Stripe uses "Idempotent-Replayed: true"
// historically; we match that semantic with the X- prefix to mark it
// as a non-standard extension.
func replayCached(c *gin.Context, cached *domain.IdempotencyResult) {
	c.Header("X-Idempotency-Replayed", "true")
	c.Data(cached.StatusCode, "application/json", []byte(cached.Body))
}

// mustMarshal panics on json.Marshal error. Reserved for fixed-shape
// DTO structs whose fields are all directly-marshalable types — the
// marshal cannot fail in practice, but the panic surfaces an
// impossible-condition violation loudly if a future field addition
// introduces a non-serialisable type (custom MarshalJSON that errors,
// pointer cycle, channel/func type). Without this guard, a `_` discard
// would commit an empty body to both the HTTP response AND the
// idempotency cache silently.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// In production this is unreachable for the call sites in this
		// package. If it ever fires, the panic is the right answer:
		// the response would otherwise be empty and the idempotency
		// cache would store the empty body for 24h replays.
		panic(fmt.Sprintf("impossible: marshal %T: %v", v, err))
	}
	return b
}

// shouldCacheStatus is the Stripe-style policy for which response
// status codes get cached for idempotency replay. Only 2xx (success)
// and 5xx (server error — clients SHOULD retry) are cached. 4xx
// (validation errors, sold-out, etc.) are NOT cached — caching them
// would burn the idempotency key for 24h on a typo'd request, even
// though the client could correct the body and legitimately retry
// with the same key.
//
// Stripe's documented behavior (https://stripe.com/blog/idempotency)
// is the reference; AWS API Gateway and Shopify follow the same
// pattern.
func shouldCacheStatus(status int) bool {
	return (status >= 200 && status < 300) || status >= 500
}

type BookingHandler interface {
	HandleBook(c *gin.Context)
	HandleGetOrder(c *gin.Context)
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

func (h *bookingHandler) HandleListBookings(c *gin.Context) {
	ctx := c.Request.Context()

	var params dto.ListBookingsQueryParams
	if err := c.ShouldBindQuery(&params); err != nil {
		log.Warn(ctx, "invalid history query params", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid query parameters"})
		return
	}

	if params.Page < 1 {
		params.Page = 1
	}
	if params.Size < 1 {
		params.Size = 10
	}

	orders, total, err := h.service.GetBookingHistory(ctx, params.Page, params.Size, params.StatusFilter())
	if err != nil {
		log.Error(ctx, "GetBookingHistory failed", tag.Error(err))
		status, public := mapError(err)
		c.JSON(status, dto.ErrorResponse{Error: public})
		return
	}

	c.JSON(http.StatusOK, dto.ListBookingsResponseFromDomain(orders, total, params.Page, params.Size))
}

func (h *bookingHandler) HandleBook(c *gin.Context) {
	ctx := c.Request.Context()

	// Step 1: validate Idempotency-Key (length + ASCII printable). Done
	// BEFORE reading the body so a malformed key short-circuits to 400
	// without burning a body read.
	idempotencyKey := c.GetHeader("Idempotency-Key")
	if idempotencyKey != "" && !isValidIdempotencyKey(idempotencyKey) {
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{
			Error: "Idempotency-Key must be ASCII-printable and at most 128 characters",
		})
		return
	}

	// Step 2: read body bytes ONCE before the cache check. Required for
	// fingerprint computation. Body-size middleware (api/middleware/
	// body_size.go) wraps c.Request.Body with http.MaxBytesReader, so
	// GetRawData returns *http.MaxBytesError on overflow — we surface
	// that as 413 to match the middleware's contract for the
	// pre-emptive Content-Length path.
	bodyBytes, err := c.GetRawData()
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			c.JSON(http.StatusRequestEntityTooLarge, dto.ErrorResponse{
				Error: "request body exceeds size limit",
			})
			return
		}
		log.Warn(ctx, "failed to read request body", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid request body"})
		return
	}

	// Step 3: idempotency cache lookup (only when key is present). The
	// fingerprint comparison enforces Stripe's contract:
	//   same key + same body  → replay cached response (200/202/...)
	//   same key + diff body  → 409 Conflict, body never re-processed
	//   same key + no fp      → legacy entry (pre-N4); replay + lazy
	//                           write-back so future replays validate
	//
	// Lazy write-back is documented in docs/PROJECT_SPEC §5; the brief
	// migration vulnerability window is bounded by the existing 24h
	// cache TTL.
	var fp string
	if idempotencyKey != "" {
		fp = fingerprint(bodyBytes)
		cached, cachedFP, getErr := h.idempotencyRepo.Get(ctx, idempotencyKey)
		if getErr == nil && cached != nil {
			switch cachedFP {
			case "":
				// Legacy entry — replay + write back the new fingerprint.
				// Errors on the writeback are best-effort (logged only)
				// so a Redis blip doesn't fail the in-flight request.
				observability.IdempotencyReplaysTotal.WithLabelValues("legacy_match").Inc()
				log.Info(ctx, "idempotency legacy entry replayed; writing back fingerprint",
					log.String("idempotency_key", idempotencyKey))
				if setErr := h.idempotencyRepo.Set(ctx, idempotencyKey, cached, fp); setErr != nil {
					log.Warn(ctx, "lazy fingerprint write-back failed; entry will retry on next replay",
						tag.Error(setErr),
						log.String("idempotency_key", idempotencyKey))
				}
				replayCached(c, cached)
				return
			case fp:
				// Match — replay verbatim.
				observability.IdempotencyReplaysTotal.WithLabelValues("match").Inc()
				replayCached(c, cached)
				return
			default:
				// Mismatch — Stripe's contract returns 409 Conflict.
				// The cached body is NOT returned (would mislead the
				// client into thinking their new request succeeded);
				// instead a structured error tells them to either
				// (a) use a fresh Idempotency-Key for the new body,
				// or (b) stop retrying.
				//
				// Both fingerprints are logged (they're SHA-256 hashes,
				// not bodies — no PII leak) so an operator
				// investigating a customer report of "I sent the same
				// body and got 409!" can correlate via grep on
				// idempotency_key and immediately see whether the
				// hashes diverged at the server (real client/proxy
				// bug) or are equal (fingerprinting bug — should never
				// happen, but the log makes the impossible visible).
				observability.IdempotencyReplaysTotal.WithLabelValues("mismatch").Inc()
				log.Warn(ctx, "idempotency-key reused with a different request body",
					log.String("idempotency_key", idempotencyKey),
					log.String("cached_fingerprint", cachedFP),
					log.String("incoming_fingerprint", fp))
				c.JSON(http.StatusConflict, dto.ErrorResponse{
					Error: "Idempotency-Key reused with a different request body",
				})
				return
			}
		}
		// Get error path is best-effort: log + fall through to normal
		// processing. Failing closed (rejecting the request because the
		// cache is down) would turn a Redis outage into an outright
		// outage of the booking endpoint.
		//
		// Logged at ERROR (not WARN) because while the fail-open is
		// the correct availability choice, the consequence is real:
		// for the duration of a Redis outage, idempotency guarantees
		// are suspended — same-key/different-body becomes processable.
		// The companion `idempotency_cache_errors_total` counter (in
		// the instrumented decorator) gives operators a metric-side
		// signal so the WARN/ERROR distinction isn't load-bearing for
		// alerting.
		if getErr != nil {
			log.Error(ctx, "idempotency cache Get failed; processing as cache-miss (idempotency briefly suspended)",
				tag.Error(getErr),
				log.String("idempotency_key", idempotencyKey))
		}
	}

	// Step 4: re-feed body so ShouldBindJSON can parse the bytes we
	// just consumed. Wrapping with bytes.NewReader is sufficient — by
	// the time we reach here, MaxBytesReader has already bounded the
	// bytes; the inner ShouldBindJSON read just iterates an in-memory
	// buffer.
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var req dto.BookingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warn(ctx, "invalid book request body", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid request body"})
		return
	}

	orderID, err := h.service.BookTicket(ctx, req.UserID, req.EventID, req.Quantity)

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
		// Marshal via the DTO so the wire shape is centralised — every
		// error response the API emits goes through dto.ErrorResponse.
		// The marshal cannot fail for the fixed-shape struct today, but
		// `must-`style panic on the impossible-but-not-checked branch
		// is preferable to `_` discard: the next reviewer to add a
		// non-serialisable field (custom MarshalJSON, pointer cycle)
		// gets a loud test failure instead of a silent empty body
		// committed to both the response AND the idempotency cache.
		errJSON := mustMarshal(dto.ErrorResponse{Error: publicMsg})
		statusCode = status
		body = string(errJSON)
	} else {
		// 202 Accepted — Redis-side deduct succeeded (the load-shed
		// gate); the rest of the lifecycle (DB persist, payment
		// charge, saga) is async. The client polls
		// `GET /api/v1/orders/:id` for the terminal status.
		statusCode = http.StatusAccepted
		acceptedJSON := mustMarshal(dto.BookingAcceptedResponse{
			OrderID: orderID,
			Status:  dto.BookingStatusProcessing,
			Message: "booking accepted, awaiting confirmation",
			Links: dto.BookingLinks{
				Self: orderSelfLinkPrefix + orderID.String(),
			},
		})
		body = string(acceptedJSON)
	}

	// Cache the result for idempotency replay. Per Stripe's convention
	// 4xx validation errors are NOT cached — a typo'd body must not
	// burn the idempotency key for 24h. 2xx (the success path) and 5xx
	// (server errors clients should retry) ARE cached.
	if idempotencyKey != "" && shouldCacheStatus(statusCode) {
		if setErr := h.idempotencyRepo.Set(ctx, idempotencyKey, &domain.IdempotencyResult{
			StatusCode: statusCode,
			Body:       body,
		}, fp); setErr != nil {
			log.Warn(ctx, "idempotency cache Set failed (response already sent; next retry will re-process)",
				tag.Error(setErr),
				log.String("idempotency_key", idempotencyKey),
				log.Int("body_size_bytes", len(body)),
			)
		}
	}

	c.Data(statusCode, "application/json", []byte(body))
}

// HandleGetOrder is the polling endpoint for a freshly-accepted
// booking. Clients receive an `order_id` from `POST /book` and poll
// here for the terminal status (confirmed / failed / compensated).
//
// 404 contract: the worker persists the order row asynchronously (~ms
// after the 202 returns). Until then, GET returns 404 — clients
// should retry with a short backoff. After the row exists, every
// subsequent GET returns the latest status.
//
// No auth today — anyone with the order_id can read. N9 will add
// JWT + ownership check (the order belongs to its user_id). Documented
// as a known gap in PROJECT_SPEC §6.
func (h *bookingHandler) HandleGetOrder(c *gin.Context) {
	ctx := c.Request.Context()

	rawID := c.Param("id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		log.Warn(ctx, "invalid order id parameter",
			tag.Error(err),
			log.String("raw_id", rawID),
		)
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid order id"})
		return
	}

	order, err := h.service.GetOrder(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrOrderNotFound) {
			// Distinct log level from upstream errors — 404 is the
			// expected path during the brief post-202 window before
			// the worker persists.
			log.Debug(ctx, "order not found (may be in async-processing window)",
				tag.OrderID(id))
			c.JSON(http.StatusNotFound, dto.ErrorResponse{Error: "order not found"})
			return
		}
		log.Error(ctx, "GetOrder failed", tag.Error(err), tag.OrderID(id))
		status, public := mapError(err)
		c.JSON(status, dto.ErrorResponse{Error: public})
		return
	}

	c.JSON(http.StatusOK, dto.OrderResponseFromDomain(order))
}

func (h *bookingHandler) HandleCreateEvent(c *gin.Context) {
	ctx := c.Request.Context()

	var req dto.CreateEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warn(ctx, "invalid create event request", tag.Error(err))
		c.JSON(http.StatusBadRequest, dto.ErrorResponse{Error: "invalid request body"})
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
		c.JSON(status, dto.ErrorResponse{Error: public})
		return
	}

	c.JSON(http.StatusCreated, dto.EventResponseFromDomain(event))
}

func (h *bookingHandler) HandleViewEvent(c *gin.Context) {
	// 記錄進入頁面的人數 (Conversion: Page Views)
	observability.PageViewsTotal.WithLabelValues("event_detail").Inc()
	c.JSON(http.StatusOK, gin.H{"message": "View event", "event_id": c.Param("id")})
}

// RegisterRoutes wires the booking endpoints under a versioned router
// group (typically /api/v1). Operational endpoints (/livez, /readyz)
// live in the ops subpackage and register at the engine root, NOT
// under this group — k8s probe targets must not move with API
// versioning.
func RegisterRoutes(r *gin.RouterGroup, handler BookingHandler) {
	r.POST("/book", handler.HandleBook)
	r.GET("/orders/:id", handler.HandleGetOrder)
	r.GET("/history", handler.HandleListBookings)
	r.POST("/events", handler.HandleCreateEvent)
	r.GET("/events/:id", handler.HandleViewEvent)
}
