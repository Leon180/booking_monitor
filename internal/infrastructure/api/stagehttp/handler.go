package stagehttp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/api/dto"
)

// V1Prefix is the API version prefix used by all stagehttp handlers.
// Exported so cmd binaries can inject it into route registration
// (matches the apiV1Prefix const in cmd/booking-cli-stage1/main.go).
const V1Prefix = "/api/v1"

// HandleBook is the Stage 1-3 POST /api/v1/book handler. Mirrors
// the Stage 4 dto contract:
//
//	202 Accepted with {order_id, status:"reserved", reserved_until,
//	                   expires_in_seconds, links.{self,pay}}
//
// All four stages return the same shape so scripts/k6_two_step_flow.js
// runs unmodified.
func HandleBook(svc booking.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req dto.BookingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
			return
		}

		order, err := svc.BookTicket(c.Request.Context(), req.UserID, req.TicketTypeID, req.Quantity)
		if err != nil {
			status, msg := MapBookingError(err)
			c.JSON(status, gin.H{"error": msg})
			return
		}

		expiresIn := int(time.Until(order.ReservedUntil()).Seconds())
		if expiresIn < 0 {
			expiresIn = 0
		}
		c.JSON(http.StatusAccepted, dto.BookingAcceptedResponse{
			OrderID:          order.ID(),
			Status:           dto.BookingStatusReserved,
			Message:          "reservation created; complete payment via links.pay before reserved_until",
			ReservedUntil:    order.ReservedUntil(),
			ExpiresInSeconds: expiresIn,
			Links: dto.BookingLinks{
				Self: fmt.Sprintf("%s/orders/%s", V1Prefix, order.ID()),
				Pay:  fmt.Sprintf("%s/orders/%s/pay", V1Prefix, order.ID()),
			},
		})
	}
}

// HandleGetOrder is the Stage 1-3 GET /api/v1/orders/:id handler.
// Returns the persisted order or 404 if not found.
func HandleGetOrder(svc booking.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := uuid.Parse(idStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
			return
		}

		order, err := svc.GetOrder(c.Request.Context(), id)
		if err != nil {
			status, msg := MapBookingError(err)
			c.JSON(status, gin.H{"error": msg})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"order_id":          order.ID(),
			"status":            string(order.Status()),
			"amount_cents":      order.AmountCents(),
			"currency":          order.Currency(),
			"payment_intent_id": order.PaymentIntentID(),
			"reserved_until":    order.ReservedUntil(),
		})
	}
}

// HandlePayIntent is the Stage 1-3 POST /api/v1/orders/:id/pay
// handler. Generates a fake `pi_<intent_prefix>_<uuid7>` intent id
// and persists it on the order row via UPDATE WHERE
// payment_intent_id IS NULL — atomic idempotency: a repeat /pay
// returns the SAME intent id (matches Stage 4's gateway-side
// idempotency).
//
// Status guards mirror Stage 4 (per Codex round-2 P2.1, the mock-
// payment contract is apples-to-apples):
//   - 404 if order doesn't exist
//   - 409 if status != awaiting_payment (already terminal)
//   - 409 if reserved_until elapsed (TTL ran out before /pay)
//
// The atomic TTL guard (`AND reserved_until > NOW()` in the UPDATE
// itself, not just in the eligibility read) closes the read-then-
// write race that Codex flagged in slice-4 P2 — without it, a
// reservation expiring between read and write would silently
// persist an intent for an already-expired reservation. RowsAffected=0
// triggers a re-read with proper disambiguation: concurrent /pay
// won (return same id), status changed terminal (409 not eligible),
// or TTL elapsed during the race (409 expired).
//
// intentPrefix is per-stage — Stage 1 uses "pi_stage1_", Stage 2
// "pi_stage2_", etc. — so a comparison.md analyst grep-ing the DB
// can attribute orders to their producing binary.
func HandlePayIntent(db *sql.DB, intentPrefix string) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		orderID, err := uuid.Parse(idStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
			return
		}

		// Single-row read first: status guards + read existing
		// intent_id (so a re-pay is idempotent without taking a
		// row lock).
		var (
			status        string
			currentIntent sql.NullString
			amountCents   int64
			currency      string
			reservedUntil time.Time
		)
		err = db.QueryRowContext(c.Request.Context(), `
			SELECT status, payment_intent_id, amount_cents, currency, reserved_until
			  FROM orders
			 WHERE id = $1`,
			orderID).Scan(&status, &currentIntent, &amountCents, &currency, &reservedUntil)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read order"})
			return
		}
		if status != string(domain.OrderStatusAwaitingPayment) {
			c.JSON(http.StatusConflict, gin.H{"error": "order not in awaiting_payment state: " + status})
			return
		}
		if !time.Now().Before(reservedUntil) {
			c.JSON(http.StatusConflict, gin.H{"error": "reservation expired"})
			return
		}

		// Idempotent return: if the order already has an intent,
		// reuse it. Stage 4's gateway is idempotent on order_id;
		// matching that contract here.
		if currentIntent.Valid && currentIntent.String != "" {
			c.JSON(http.StatusOK, gin.H{
				"order_id":          orderID,
				"payment_intent_id": currentIntent.String,
				"client_secret":     "stub_secret_" + currentIntent.String,
				"amount_cents":      amountCents,
				"currency":          currency,
			})
			return
		}

		// Generate + persist a fresh intent. Filter on
		//   payment_intent_id IS NULL  — concurrent /pay can't double-write
		//   status = 'awaiting_payment' — terminal states reject
		//   reserved_until > NOW()     — atomic TTL guard
		// (Codex round-1 P2 + Codex round-2 P2; see Compensator
		// docstring for the failure-path counterpart.)
		intentSuffix, err := uuid.NewV7()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "mint intent id"})
			return
		}
		intentID := intentPrefix + intentSuffix.String()
		res, err := db.ExecContext(c.Request.Context(), `
			UPDATE orders
			   SET payment_intent_id = $1
			 WHERE id = $2
			   AND payment_intent_id IS NULL
			   AND status = 'awaiting_payment'
			   AND reserved_until > NOW()`,
			intentID, orderID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "persist intent"})
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// RowsAffected=0 disambiguation. Three causes:
			//   (a) concurrent /pay won — row now has an intent; return it idempotently
			//   (b) reserved_until elapsed between read and write — return 409 expired
			//   (c) status changed terminal — return 409 not eligible
			var (
				reReadStatus     string
				reReadIntent     sql.NullString
				reReadReservedAt time.Time
			)
			err = db.QueryRowContext(c.Request.Context(), `
				SELECT status, payment_intent_id, reserved_until
				  FROM orders
				 WHERE id = $1`,
				orderID).Scan(&reReadStatus, &reReadIntent, &reReadReservedAt)
			if errors.Is(err, sql.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
				return
			}
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "re-read order after race"})
				return
			}
			if reReadStatus != string(domain.OrderStatusAwaitingPayment) {
				c.JSON(http.StatusConflict, gin.H{"error": "order not in awaiting_payment state: " + reReadStatus})
				return
			}
			if !time.Now().Before(reReadReservedAt) {
				c.JSON(http.StatusConflict, gin.H{"error": "reservation expired"})
				return
			}
			// Status awaiting + reserved future + UPDATE rejected ⇒
			// concurrent /pay won. If intent is STILL null here, our
			// UPDATE should have matched — surface as 500 so a
			// regression in this shape isn't silent.
			if !reReadIntent.Valid || reReadIntent.String == "" {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "race fallback: unexpected null intent on eligible row"})
				return
			}
			intentID = reReadIntent.String
		}

		c.JSON(http.StatusOK, gin.H{
			"order_id":          orderID,
			"payment_intent_id": intentID,
			"client_secret":     "stub_secret_" + intentID,
			"amount_cents":      amountCents,
			"currency":          currency,
		})
	}
}

// HandleTestConfirm is the Stage 1-3 POST /test/payment/confirm/:id?
// outcome=succeeded|failed handler. Mirrors Stage 4's mock-confirm
// contract:
//
//   - succeeded: race-aware UPDATE to status='paid' with the same
//     predicate Stage 4 uses (`status='awaiting_payment' AND
//     payment_intent_id IS NOT NULL AND reserved_until > NOW()`).
//   - failed: pre-check payment_intent_id IS NOT NULL (must call
//     /pay first — Codex round-2 P2.1 contract), then call
//     compensator.Compensate. Compensator is the architectural
//     seam each stage supplies (SQL-only for Stage 1; Redis-revert+
//     SQL for Stage 2; etc.).
//
// ErrCompensateNotEligible from the Compensator → 409 (concurrent
// /confirm-succeeded landed between pre-check and lock; expected
// race, not an error).
func HandleTestConfirm(db *sql.DB, comp Compensator) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		orderID, err := uuid.Parse(idStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order id"})
			return
		}
		outcome := c.Query("outcome")
		if outcome != "succeeded" && outcome != "failed" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "outcome must be 'succeeded' or 'failed'"})
			return
		}

		if outcome == "succeeded" {
			res, err := db.ExecContext(c.Request.Context(), `
				UPDATE orders
				   SET status = 'paid'
				 WHERE id = $1
				   AND status = 'awaiting_payment'
				   AND payment_intent_id IS NOT NULL
				   AND reserved_until > NOW()`,
				orderID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "mark paid"})
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "order not eligible for paid (already terminal / no /pay / expired)"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "paid"})
			return
		}

		// outcome == "failed" — pre-check intent contract (Codex
		// round-2 P2.1: /confirm requires /pay-first; sweeper has
		// no such contract because abandoned-without-/pay is a valid
		// abandon path), then call the per-stage Compensator.
		var hasIntent sql.NullString
		err = db.QueryRowContext(c.Request.Context(),
			"SELECT payment_intent_id FROM orders WHERE id = $1",
			orderID).Scan(&hasIntent)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read order"})
			return
		}
		if !hasIntent.Valid || hasIntent.String == "" {
			c.JSON(http.StatusConflict, gin.H{"error": "order has no payment_intent — must call /pay before confirm"})
			return
		}

		err = comp.Compensate(c.Request.Context(), orderID)
		switch {
		case err == nil:
			c.JSON(http.StatusOK, gin.H{"status": "compensated"})
		case errors.Is(err, ErrCompensateNotEligible):
			status, msg := MapBookingError(err)
			c.JSON(status, gin.H{"error": msg})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "compensation failed"})
		}
	}
}

// avoid unused-context import in some go versions when the package
// is consumed without all symbols referenced; ctx is used inside
// the handlers above.
var _ = context.Background
