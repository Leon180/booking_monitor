// Package testapi hosts NON-PRODUCTION HTTP endpoints used to simulate
// external systems during development, integration testing, and demos.
//
// EVERY route mounted from this package is gated by
// `cfg.Server.EnableTestEndpoints` — production deployments leave the
// flag false and the entire route group is never registered, which
// means a 404 (not even a 401) for any request to /test/*. This makes
// the surface impossible to enable accidentally — the operator must
// explicitly set the env var.
//
// Why a separate package: keeps the demo / test scaffolding visually
// distinct from the production booking / webhook surface so a future
// reader doesn't mistake `/test/payment/confirm/...` for a production
// route. Also lets the entire package go away in a future build by
// dropping the import — no production code depends on it.
package testapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"booking_monitor/internal/application/payment"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/webhook"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// Handler is the test-only payment-confirm handler. It exists so
// developers + integration tests can simulate "Stripe Elements client
// confirmed the payment" without an actual provider — D4 `/pay`
// stays silent (no auto-emit, plan v5 §Step 7), and clients of the
// flow drive the webhook deliberately via this endpoint.
//
// What it does:
//  1. Read the order via the order repo (to grab payment_intent_id +
//     amount_cents + currency for the envelope).
//  2. Build a Stripe-shape Envelope with metadata.order_id set.
//  3. Sign the envelope bytes with the webhook secret (same path the
//     real provider would use).
//  4. POST internally to `/webhook/payment` so the FULL signature
//     verification + dispatch pipeline runs — exactly what a real
//     Stripe webhook would exercise.
//
// What it does NOT do:
//   - Bypass the signature verifier. The point is full-pipeline
//     fidelity for demos. A test that wants to bypass goes via the
//     service-level unit tests, NOT via this endpoint.
//   - Persist anything itself. All state changes happen inside the
//     real WebhookService.
type Handler struct {
	orderRepo domain.OrderRepository
	secret    []byte
	targetURL string // e.g. "http://127.0.0.1:8080/webhook/payment"
	client    *http.Client
	log       *mlog.Logger
	now       func() time.Time
}

// NewHandler wires the test handler. `targetURL` is the loopback URL
// for our own webhook endpoint — we form-feed our signed envelope
// back into the production handler so the verifier + dispatcher run.
func NewHandler(orderRepo domain.OrderRepository, secret []byte, targetURL string, logger *mlog.Logger) *Handler {
	return &Handler{
		orderRepo: orderRepo,
		secret:    secret,
		targetURL: targetURL,
		// Short timeout — we're hitting our own loopback. Anything
		// over a couple of seconds is a real bug.
		client: &http.Client{Timeout: 5 * time.Second},
		log:    logger.With(mlog.String("component", "testapi")),
		now:    time.Now,
	}
}

// HandleConfirmPayment is the gin handler for
// `POST /test/payment/confirm/:order_id?outcome=succeeded|failed`.
//
// Returns 200 with `{"forwarded": true, "webhook_status": <int>}` on
// successful loopback (regardless of webhook outcome — operator can
// inspect the actual webhook outcome via the returned status code or
// the order's persisted state).
func (h *Handler) HandleConfirmPayment(c *gin.Context) {
	ctx := c.Request.Context()

	orderID, err := uuid.Parse(c.Param("order_id"))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid order_id"})
		return
	}
	outcome := c.Query("outcome")
	switch outcome {
	case "succeeded", "failed":
	default:
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "outcome must be 'succeeded' or 'failed'"})
		return
	}

	order, err := h.orderRepo.GetByID(ctx, orderID)
	if err != nil {
		if errors.Is(err, domain.ErrOrderNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		h.log.Error(ctx, "testapi: GetByID failed", tag.OrderID(orderID), tag.Error(err))
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	intentID := order.PaymentIntentID()
	if intentID == "" {
		// This endpoint is meant to be called AFTER /pay. Failing
		// fast surfaces "you forgot to call /pay" loudly.
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "order has no payment_intent_id (call /pay first)"})
		return
	}

	env := buildEnvelope(orderID, intentID, order.AmountCents(), order.Currency(), outcome, h.now())
	body, err := json.Marshal(env)
	if err != nil {
		h.log.Error(ctx, "testapi: marshal envelope", tag.OrderID(orderID), tag.Error(err))
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	sigHeader := signEnvelope(h.secret, body, h.now())
	status, err := h.postToWebhook(ctx, body, sigHeader)
	if err != nil {
		h.log.Error(ctx, "testapi: forward to webhook failed", tag.OrderID(orderID), tag.Error(err))
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "webhook delivery failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"forwarded": true, "webhook_status": status})
}

// buildEnvelope constructs a Stripe-shape Envelope for the given
// outcome. Mirrors what the mock gateway / a real Stripe redelivery
// would emit.
func buildEnvelope(orderID uuid.UUID, intentID string, amountCents int64, currency, outcome string, now time.Time) payment.Envelope {
	eventType := payment.EventTypePaymentIntentSucceeded
	intentStatus := "succeeded"
	var lastError *payment.PaymentIntentLastError
	if outcome == "failed" {
		eventType = payment.EventTypePaymentIntentPaymentFailed
		intentStatus = "requires_payment_method"
		lastError = &payment.PaymentIntentLastError{
			Code:    "card_declined",
			Message: "simulated decline from /test/payment/confirm",
		}
	}
	return payment.Envelope{
		ID:       "evt_test_" + uuid.NewString(),
		Type:     eventType,
		Created:  now.Unix(),
		LiveMode: false,
		Data: payment.EnvelopeData{
			Object: payment.PaymentIntentObject{
				ID:               intentID,
				Status:           intentStatus,
				AmountCents:      amountCents,
				Currency:         currency,
				Metadata:         map[string]string{payment.MetadataKeyOrderID: orderID.String()},
				LastPaymentError: lastError,
			},
		},
	}
}

// signEnvelope produces the Stripe-shape `t=<unix>,v1=<hex>` header
// over the marshaled envelope bytes. Same algorithm the verifier
// expects.
func signEnvelope(secret []byte, body []byte, now time.Time) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(strconv.AppendInt(nil, now.Unix(), 10))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "t=" + strconv.FormatInt(now.Unix(), 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// postToWebhook delivers the signed envelope to our own webhook
// endpoint over loopback HTTP.
func (h *Handler) postToWebhook(ctx context.Context, body []byte, sigHeader string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.targetURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("testapi.postToWebhook NewRequest: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(webhook.SignatureHeader, sigHeader)
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("testapi.postToWebhook Do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// RegisterRoutes mounts the test endpoint at the engine root. ONLY
// call this when `cfg.Server.EnableTestEndpoints` is true.
func RegisterRoutes(r *gin.Engine, h *Handler) {
	r.POST("/test/payment/confirm/:order_id", h.HandleConfirmPayment)
}
