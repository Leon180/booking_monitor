package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"booking_monitor/internal/application/payment"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// MaxBodyBytes caps the inbound webhook body. 64 KiB is comfortable
// headroom over Stripe's documented 256-byte typical envelope and the
// largest realistic payload (`payment_intent` with full metadata +
// charge object embedded) — anything larger is hostile or buggy.
//
// Why a body cap matters: without one, a single attacker can hold
// open a request and stream gigabytes into the verifier's HMAC
// computation. The cap fail-fasts at the io.LimitReader boundary
// before any HMAC bytes hash — the failure looks like a 400 with no
// secret-leakage risk.
const MaxBodyBytes = 64 << 10

// PaymentService is the application-side interface the handler calls.
// Defined here (consumer side) per the project's small-interface
// convention. Production wiring binds it to
// `payment.NewWebhookService(...)`; tests bind a fake.
type PaymentService interface {
	HandleWebhook(ctx context.Context, env payment.Envelope) error
}

// HandlerMetrics records the outer-handler counters that the
// PaymentService doesn't see (signature failures happen before the
// service is even invoked).
type HandlerMetrics interface {
	// SignatureInvalid fires when verification fails; reason is one
	// of "missing" / "malformed" / "skew_exceeded" / "mismatch" /
	// "config_error" (see ClassifySignatureError).
	SignatureInvalid(reason string)
	// Received counts only events that reached the dispatcher with a
	// "malformed" label — the service's own Received(...) covers the
	// post-parse outcomes. Splitting here avoids double-counting.
	BodyMalformed()
}

// Handler is the gin-side webhook entry point. Constructed via
// NewHandler in package wiring. Stateless aside from the secret +
// tolerance + clock — safe to share across goroutines.
type Handler struct {
	svc       PaymentService
	secret    []byte
	tolerance time.Duration
	metrics   HandlerMetrics
	log       *mlog.Logger
	now       func() time.Time
}

// NewHandler wires the dependencies. `secret` MUST be non-empty;
// caller asserts that at config-load time so the application aborts
// startup rather than running with a no-op verifier.
//
// `tolerance` is typically 5 minutes — Stripe's documented default
// — and configurable so tests can dial it down.
func NewHandler(svc PaymentService, secret []byte, tolerance time.Duration, metrics HandlerMetrics, logger *mlog.Logger) *Handler {
	return &Handler{
		svc:       svc,
		secret:    secret,
		tolerance: tolerance,
		metrics:   metrics,
		log:       logger.With(mlog.String("component", "payment_webhook_handler")),
		now:       time.Now,
	}
}

// HandlePayment is the gin handler. The pipeline:
//  1. Read raw body bytes (LIMITED to MaxBodyBytes).
//  2. Verify Stripe-Signature against the raw bytes (NEVER against
//     a re-marshaled struct — JSON marshalling is not byte-stable).
//  3. JSON-unmarshal the now-trusted body.
//  4. Dispatch to the application service.
//  5. Map application sentinels to HTTP codes.
//
// Why steps 1–2 sit at the handler edge (instead of a middleware):
// the verifier needs the raw bytes; a middleware that read the body
// would force every handler to read again or rely on body re-injection
// magic. Webhook is a single handler — cleaner inlined.
func (h *Handler) HandlePayment(c *gin.Context) {
	ctx := c.Request.Context()

	// Read with a body cap larger than MaxBodyBytes so we can DETECT
	// truncation: if the cap fired, the read would top out at exactly
	// MaxBodyBytes with no error from io.ReadAll. Reading 1 extra byte
	// lets the post-read length check catch oversize bodies explicitly
	// instead of letting them silently 401-mismatch the verifier (the
	// HMAC over a truncated body is correct, but operators investigating
	// the resulting "mismatch" spike would be misled toward a secret-
	// rotation hypothesis).
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, MaxBodyBytes+1))
	if err != nil {
		h.log.Warn(ctx, "webhook: body read failed", tag.Error(err))
		h.metrics.BodyMalformed()
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "body read failed"})
		return
	}
	if len(raw) > MaxBodyBytes {
		h.log.Warn(ctx, "webhook: body exceeds MaxBodyBytes — refusing to verify truncated payload",
			mlog.Int("max_bytes", MaxBodyBytes))
		h.metrics.BodyMalformed()
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
		return
	}

	header := c.GetHeader(SignatureHeader)
	if err := VerifySignature(h.secret, raw, header, h.now(), h.tolerance); err != nil {
		reason := ClassifySignatureError(err)
		h.log.Warn(ctx, "webhook: signature invalid",
			mlog.String("reason", reason),
			tag.Error(err))
		h.metrics.SignatureInvalid(reason)
		// Config errors are 500 (loud, on-call) — every other reason
		// is 401. The empty-secret branch is a startup-config bug
		// the operator must fix; pretending it's a 401 would hide it.
		if reason == "config_error" {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	var env payment.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		h.log.Warn(ctx, "webhook: body parse failed", tag.Error(err))
		h.metrics.BodyMalformed()
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "body parse failed"})
		return
	}

	if err := h.svc.HandleWebhook(ctx, env); err != nil {
		h.mapServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

// mapServiceError translates the application-layer typed sentinels to
// HTTP codes. The mapping is documented in plan v5 §A "HTTP contract":
//
//	ErrWebhookUnknownIntent     500 — provider retries; rescues SetPaymentIntentID race
//	ErrWebhookIntentMismatch    500 — provider retries; pages on-call
//	ErrWebhookUnexpectedStatus  409 — order in non-Pattern-A status
//	other (DB transient etc.)   500 — provider retries
//
// We must NOT leak internal error text — log server-side, return a
// generic public message keyed off the sentinel.
func (h *Handler) mapServiceError(c *gin.Context, err error) {
	// Service-layer error is already logged inside the service; we
	// only re-log the mapping decision so a future operator can
	// trace HTTP code → sentinel without grepping two layers.
	switch {
	case errors.Is(err, payment.ErrWebhookUnknownIntent):
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "unknown intent — please retry"})
	case errors.Is(err, payment.ErrWebhookIntentMismatch):
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "intent mismatch — please retry"})
	case errors.Is(err, payment.ErrWebhookUnexpectedStatus):
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "order in unexpected status"})
	default:
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}
