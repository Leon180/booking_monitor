//go:build integration

package pgintegration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booking_monitor/internal/infrastructure/api/stagehttp"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for stagehttp.HandlePayIntent against a real
// postgres:15-alpine container. The handler-level unit tests
// (internal/infrastructure/api/stagehttp/handler_test.go) cover
// HTTP-shape concerns (invalid UUID, binding); these tests pin
// the SQL contract that the unit tests can't reach because the
// handler talks to *sql.DB directly:
//
//   - Success persists `<prefix>_<uuid7>` intent_id with the
//     supplied stage prefix
//   - Existing-intent → idempotent return of the same intent_id
//   - Order not found → 404
//   - Non-awaiting_payment status (paid / expired / compensated /
//     payment_failed) → 409
//   - **Expired reservation (reserved_until in past) → 409** —
//     pins Codex slice-4 P2 race-fix predicate
//
// The "expired between read-and-UPDATE" race itself is not
// exercised here (would need a hook between the SELECT and the
// UPDATE inside the handler). The static predicate in the UPDATE
// is what makes the race-fix correct; this test pins that the
// predicate rejects expired rows when the eligibility-read sees
// them as expired up front. The slice-4 P2 fix's atomic UPDATE
// guard `AND reserved_until > NOW()` is exercised in production
// via the live HTTP smoke + would surface as inventory drift if
// it ever regressed.

// stagehttpPayHarness wires StartPostgres + a gin.Engine with the
// HandlePayIntent route mounted. Returns the harness (for direct
// DB seeding/inspection) and the test gin engine.
func stagehttpPayHarness(t *testing.T, intentPrefix string) (*pgintegration.Harness, *gin.Engine) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/orders/:id/pay", stagehttp.HandlePayIntent(h.DB, intentPrefix))
	return h, r
}

// seedTicketTypeForPay inserts an event + ticket_type row and
// returns (eventID, ticketTypeID). Same shape as the helper in
// sync_booking_test.go but kept inline here so this test file is
// self-contained.
func seedTicketTypeForPay(t *testing.T, h *pgintegration.Harness, availableTickets int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	eventID := uuid.New()
	h.SeedEvent(t, eventID.String(), "stagehttp-pay test event", availableTickets)
	ttID, err := uuid.NewV7()
	require.NoError(t, err)
	_, err = h.DB.Exec(`
		INSERT INTO event_ticket_types (
			id, event_id, name, price_cents, currency,
			total_tickets, available_tickets, version
		) VALUES ($1::uuid, $2::uuid, 'GA', 2000, 'usd', $3, $3, 0)`,
		ttID.String(), eventID.String(), availableTickets)
	require.NoError(t, err, "seed event_ticket_types")
	return eventID, ttID
}

// seedOrderRow inserts an order row directly with the given
// status + reserved_until. Lets each test fully control the
// preconditions (active vs expired, awaiting vs paid, etc.) —
// SeedReservation hard-codes reserved_until to NOW + 15min.
//
// userID must be distinct across seeds against the same event
// (uq_orders_user_event partial unique index post-D4.1 rejects
// duplicate active orders for the same user × event pair).
func seedOrderRow(t *testing.T, h *pgintegration.Harness, eventID, ttID uuid.UUID, userID int, status string, reservedUntil time.Time, paymentIntentID string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	var intentArg interface{}
	if paymentIntentID == "" {
		intentArg = nil
	} else {
		intentArg = paymentIntentID
	}
	_, err = h.DB.Exec(`
		INSERT INTO orders (
			id, event_id, ticket_type_id, user_id, quantity, status,
			created_at, updated_at, reserved_until,
			amount_cents, currency, payment_intent_id
		) VALUES (
			$1::uuid, $2::uuid, $3::uuid, $4, 1, $5,
			NOW(), NOW(), $6,
			2000, 'usd', $7
		)`, id.String(), eventID.String(), ttID.String(), userID, status,
		reservedUntil, intentArg)
	require.NoError(t, err, "seed order")
	return id
}

// callPay sends POST /api/v1/orders/:id/pay against the test
// router and returns the response code + decoded body.
func callPay(t *testing.T, r *gin.Engine, orderID uuid.UUID) (int, map[string]interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+orderID.String()+"/pay",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	body := map[string]interface{}{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body
}

// readOrderIntent inspects orders.payment_intent_id directly.
func readOrderIntent(t *testing.T, h *pgintegration.Harness, id uuid.UUID) string {
	t.Helper()
	var intent *string
	require.NoError(t, h.DB.QueryRow(
		"SELECT payment_intent_id FROM orders WHERE id = $1::uuid",
		id.String()).Scan(&intent))
	if intent == nil {
		return ""
	}
	return *intent
}

// ────────────────────────────────────────────────────────────────
// Tests
// ────────────────────────────────────────────────────────────────

// TestStageHTTPPay_Success_PersistsPrefixedIntent — happy path:
// awaiting_payment + future reserved_until + null intent →
// 200 with intent_id starting with the supplied prefix; row
// updated in PG with the same intent.
func TestStageHTTPPay_Success_PersistsPrefixedIntent(t *testing.T) {
	const prefix = "pi_stage1_"
	h, r := stagehttpPayHarness(t, prefix)

	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), "")

	code, body := callPay(t, r, orderID)
	require.Equal(t, http.StatusOK, code, "body=%v", body)

	intentID, _ := body["payment_intent_id"].(string)
	require.NotEmpty(t, intentID)
	assert.True(t, strings.HasPrefix(intentID, prefix),
		"intent should be prefixed with %q; got %q", prefix, intentID)
	assert.Contains(t, body, "client_secret")
	assert.EqualValues(t, 2000, body["amount_cents"])
	assert.Equal(t, "usd", body["currency"])

	// PG row matches.
	persisted := readOrderIntent(t, h, orderID)
	assert.Equal(t, intentID, persisted,
		"intent persisted in PG should match response")
}

// TestStageHTTPPay_Idempotent_ReturnsSameIntent — order already
// has an intent (from a prior /pay) → re-pay returns the SAME
// intent_id without overwriting.
func TestStageHTTPPay_Idempotent_ReturnsSameIntent(t *testing.T) {
	const prefix = "pi_stage1_"
	h, r := stagehttpPayHarness(t, prefix)

	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	preExisting := "pi_stage1_pre-existing-intent"
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), preExisting)

	code, body := callPay(t, r, orderID)
	require.Equal(t, http.StatusOK, code, "body=%v", body)

	got, _ := body["payment_intent_id"].(string)
	assert.Equal(t, preExisting, got,
		"re-pay should return the pre-existing intent (idempotent)")

	// PG row not modified.
	persisted := readOrderIntent(t, h, orderID)
	assert.Equal(t, preExisting, persisted)
}

// TestStageHTTPPay_OrderNotFound — random UUID → 404.
func TestStageHTTPPay_OrderNotFound(t *testing.T) {
	_, r := stagehttpPayHarness(t, "pi_stage1_")
	missingID, err := uuid.NewV7()
	require.NoError(t, err)
	code, body := callPay(t, r, missingID)
	assert.Equal(t, http.StatusNotFound, code, "body=%v", body)
}

// TestStageHTTPPay_NotAwaitingPayment — order in any non-
// awaiting_payment state → 409. Covers the contract that /pay
// is only valid during the reservation window.
func TestStageHTTPPay_NotAwaitingPayment(t *testing.T) {
	for _, status := range []string{"paid", "compensated", "payment_failed", "expired"} {
		t.Run(status, func(t *testing.T) {
			h, r := stagehttpPayHarness(t, "pi_stage1_")
			eventID, ttID := seedTicketTypeForPay(t, h, 5)
			// reserved_until in the future so this case isolates the
			// status check from the TTL check.
			orderID := seedOrderRow(t, h, eventID, ttID, 1, status,
				time.Now().Add(15*time.Minute), "pi_stage1_existing")

			code, body := callPay(t, r, orderID)
			assert.Equal(t, http.StatusConflict, code, "body=%v", body)
			errMsg, _ := body["error"].(string)
			assert.Contains(t, errMsg, "awaiting_payment",
				"error should name the expected state; got %q", errMsg)
		})
	}
}

// TestStageHTTPPay_Expired — reserved_until in the past while
// status is still awaiting_payment (sweeper hasn't fired yet) →
// 409. Pins Codex slice-4 P2 race-fix predicate: the eligibility
// read sees the elapsed TTL and rejects before attempting the
// UPDATE.
func TestStageHTTPPay_Expired(t *testing.T) {
	h, r := stagehttpPayHarness(t, "pi_stage1_")
	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	// reserved_until 1 minute in the past, status still
	// awaiting_payment (sweeper hasn't run yet — exactly the
	// race window the slice-4 fix closes atomically).
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(-1*time.Minute), "")

	code, body := callPay(t, r, orderID)
	assert.Equal(t, http.StatusConflict, code, "body=%v", body)
	errMsg, _ := body["error"].(string)
	assert.Contains(t, errMsg, "expired",
		"error should mention expired reservation; got %q", errMsg)

	// PG row NOT updated — predicate rejected at eligibility read.
	persisted := readOrderIntent(t, h, orderID)
	assert.Empty(t, persisted,
		"expired-rejected /pay must NOT persist an intent")
}

// TestStageHTTPPay_PrefixIsolation — confirms that two calls with
// different stage prefixes against different orders persist
// stage-attributable intent ids. Sanity-checks the prefix
// parameter wiring; would catch a regression where the prefix
// was hard-coded back to a single value.
func TestStageHTTPPay_PrefixIsolation(t *testing.T) {
	h := pgintegration.StartPostgres(context.Background(), t)
	gin.SetMode(gin.TestMode)
	r1 := gin.New()
	r1.POST("/api/v1/orders/:id/pay", stagehttp.HandlePayIntent(h.DB, "pi_stage1_"))
	r2 := gin.New()
	r2.POST("/api/v1/orders/:id/pay", stagehttp.HandlePayIntent(h.DB, "pi_stage2_"))

	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	// Distinct user_ids — the partial unique index uq_orders_user_event
	// rejects duplicate active orders for the same user × event pair.
	orderA := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), "")
	orderB := seedOrderRow(t, h, eventID, ttID, 2, "awaiting_payment",
		time.Now().Add(15*time.Minute), "")

	codeA, bodyA := callPay(t, r1, orderA)
	require.Equal(t, http.StatusOK, codeA)
	codeB, bodyB := callPay(t, r2, orderB)
	require.Equal(t, http.StatusOK, codeB)

	intentA, _ := bodyA["payment_intent_id"].(string)
	intentB, _ := bodyB["payment_intent_id"].(string)
	assert.True(t, strings.HasPrefix(intentA, "pi_stage1_"),
		"intent A should carry stage1 prefix; got %q", intentA)
	assert.True(t, strings.HasPrefix(intentB, "pi_stage2_"),
		"intent B should carry stage2 prefix; got %q", intentB)
	assert.NotEqual(t, intentA, intentB)
}

// Compile-time hint that fmt is in scope (used by error messages
// inside the helpers when stringifying parameters).
var _ = fmt.Sprintf
