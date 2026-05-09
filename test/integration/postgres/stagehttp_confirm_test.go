//go:build integration

package pgintegration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"booking_monitor/internal/infrastructure/api/stagehttp"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for stagehttp.HandleTestConfirm against a real
// postgres:15-alpine container. Symmetric with stagehttp_pay_test.go
// — pins the SQL contract that the unit tests at
// internal/infrastructure/api/stagehttp/handler_test.go can't reach
// because the handler talks to *sql.DB directly:
//
//   outcome=succeeded path:
//   - flips awaiting_payment → paid when row is eligible (has intent
//     + reserved_until > NOW)
//   - rejects when payment_intent_id IS NULL (the /pay-first contract
//     enforced atomically in the UPDATE WHERE clause; matches Stage 4's
//     MarkPaid SQL predicate)
//   - rejects when reserved_until elapsed (TTL predicate)
//   - rejects when status isn't awaiting_payment (terminal already)
//
//   outcome=failed path:
//   - pre-checks payment_intent_id IS NOT NULL BEFORE calling the
//     Compensator (Codex round-2 P2.1 contract; sweeper has no such
//     check, /confirm-failed does)
//   - calls compensator.Compensate(ctx, orderID) on the right id
//   - missing order → 404, Compensator NOT called
//   - missing intent → 409, Compensator NOT called
//   - Compensator returns nil → 200 + {"status":"compensated"}
//   - Compensator returns ErrCompensateNotEligible → 409 (race with
//     concurrent /confirm-succeeded; expected, not an error)
//   - Compensator returns generic error → 500
//
// fakeCompensator is a recording test double — captures every
// Compensate call's orderID and lets tests configure the return
// error. Lives in this file because the unit-test fakeCompensator
// in handler_test.go is in a different package (stagehttp_test);
// duplicating ~20 LOC is cheaper than exporting it.

type fakeCompensator struct {
	calls   atomic.Int64
	gotID   atomic.Pointer[uuid.UUID]
	returns error
}

func (f *fakeCompensator) Compensate(_ context.Context, orderID uuid.UUID) error {
	f.calls.Add(1)
	f.gotID.Store(&orderID)
	return f.returns
}

func (f *fakeCompensator) lastCall() (callCount int64, lastID uuid.UUID) {
	callCount = f.calls.Load()
	if p := f.gotID.Load(); p != nil {
		lastID = *p
	}
	return
}

// stagehttpConfirmHarness wires StartPostgres + a gin engine with
// HandleTestConfirm mounted. Returns harness, gin engine, and the
// fakeCompensator the test configures.
func stagehttpConfirmHarness(t *testing.T) (*pgintegration.Harness, *gin.Engine, *fakeCompensator) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	comp := &fakeCompensator{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/test/payment/confirm/:id", stagehttp.HandleTestConfirm(h.DB, comp))
	return h, r, comp
}

// callConfirm POSTs /test/payment/confirm/:id?outcome=... against
// the test router and returns code + decoded body.
func callConfirm(t *testing.T, r *gin.Engine, orderID uuid.UUID, outcome string) (int, map[string]interface{}) {
	t.Helper()
	url := "/test/payment/confirm/" + orderID.String() + "?outcome=" + outcome
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	body := map[string]interface{}{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec.Code, body
}

// readOrderStatus inspects orders.status directly — used to verify
// the SQL-side state after a /confirm call.
func readOrderStatus(t *testing.T, h *pgintegration.Harness, id uuid.UUID) string {
	t.Helper()
	var status string
	require.NoError(t, h.DB.QueryRow(
		"SELECT status FROM orders WHERE id = $1::uuid", id.String()).Scan(&status))
	return status
}

// ────────────────────────────────────────────────────────────────
// outcome=succeeded path
// ────────────────────────────────────────────────────────────────

// Eligible row → 200 + status=paid.
func TestStageHTTPConfirm_Succeeded_FlipsToPaid(t *testing.T) {
	h, r, comp := stagehttpConfirmHarness(t)
	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), "pi_stage1_existing")

	code, body := callConfirm(t, r, orderID, "succeeded")
	require.Equal(t, http.StatusOK, code, "body=%v", body)
	assert.Equal(t, "paid", body["status"])

	assert.Equal(t, "paid", readOrderStatus(t, h, orderID),
		"PG row status should be 'paid' after successful confirm")
	assert.Zero(t, comp.calls.Load(),
		"Compensator must NOT be called on outcome=succeeded")
}

// payment_intent_id IS NULL → 409, no state change.
//
// Pins the /pay-first contract atomically (the UPDATE's
// `payment_intent_id IS NOT NULL` predicate). A client calling
// /confirm directly without first calling /pay would otherwise
// silently succeed — matches Stage 4's MarkPaid SQL.
func TestStageHTTPConfirm_Succeeded_RejectsWithoutIntent(t *testing.T) {
	h, r, comp := stagehttpConfirmHarness(t)
	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	// awaiting_payment + reserved_until future + intent NULL —
	// only the /pay-first predicate should reject.
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), "")

	code, body := callConfirm(t, r, orderID, "succeeded")
	assert.Equal(t, http.StatusConflict, code, "body=%v", body)
	errMsg, _ := body["error"].(string)
	assert.Contains(t, errMsg, "not eligible")

	assert.Equal(t, "awaiting_payment", readOrderStatus(t, h, orderID),
		"row state must NOT change on rejected /confirm")
	assert.Zero(t, comp.calls.Load(),
		"Compensator must NOT be called on outcome=succeeded")
}

// reserved_until elapsed → 409 (TTL predicate).
//
// Same race-window class as the /pay TTL test — pins the atomic
// `reserved_until > NOW()` guard in the UPDATE. Without it, a
// late confirm-succeeded landing after TTL would silently flip
// the order to paid even though the reservation expired and a
// concurrent expiry sweeper might have already begun
// compensation.
func TestStageHTTPConfirm_Succeeded_RejectsExpired(t *testing.T) {
	h, r, comp := stagehttpConfirmHarness(t)
	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(-1*time.Minute), "pi_stage1_existing")

	code, body := callConfirm(t, r, orderID, "succeeded")
	assert.Equal(t, http.StatusConflict, code, "body=%v", body)

	assert.Equal(t, "awaiting_payment", readOrderStatus(t, h, orderID),
		"expired-rejected confirm must NOT advance status")
	assert.Zero(t, comp.calls.Load(),
		"Compensator must NOT be called on outcome=succeeded")
}

// status != awaiting_payment → 409.
func TestStageHTTPConfirm_Succeeded_RejectsNonAwaiting(t *testing.T) {
	for _, status := range []string{"paid", "compensated", "payment_failed", "expired"} {
		t.Run(status, func(t *testing.T) {
			h, r, comp := stagehttpConfirmHarness(t)
			eventID, ttID := seedTicketTypeForPay(t, h, 5)
			orderID := seedOrderRow(t, h, eventID, ttID, 1, status,
				time.Now().Add(15*time.Minute), "pi_stage1_existing")

			code, body := callConfirm(t, r, orderID, "succeeded")
			assert.Equal(t, http.StatusConflict, code, "body=%v", body)

			assert.Equal(t, status, readOrderStatus(t, h, orderID),
				"row state must NOT change")
			assert.Zero(t, comp.calls.Load())
		})
	}
}

// ────────────────────────────────────────────────────────────────
// outcome=failed path
// ────────────────────────────────────────────────────────────────

// Eligible row → Compensator called with the correct orderID,
// 200 + status=compensated.
func TestStageHTTPConfirm_Failed_CallsCompensatorAfterIntentPrecheck(t *testing.T) {
	h, r, comp := stagehttpConfirmHarness(t)
	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), "pi_stage1_existing")
	// fakeCompensator returns nil by default — simulate "compensation
	// succeeded".

	code, body := callConfirm(t, r, orderID, "failed")
	require.Equal(t, http.StatusOK, code, "body=%v", body)
	assert.Equal(t, "compensated", body["status"])

	calls, lastID := comp.lastCall()
	assert.EqualValues(t, 1, calls,
		"Compensator should have been called exactly once")
	assert.Equal(t, orderID, lastID,
		"Compensator must be called with the requested order_id")
}

// payment_intent_id IS NULL → 409, Compensator NOT called.
//
// Pins Codex round-2 P2.1: /confirm has the "must call /pay first"
// contract; the precheck must happen BEFORE Compensator runs (so
// inventory isn't restored for an order that was abandoned at
// /book stage and should be picked up by the sweeper instead).
func TestStageHTTPConfirm_Failed_RejectsWithoutIntent(t *testing.T) {
	h, r, comp := stagehttpConfirmHarness(t)
	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), "")

	code, body := callConfirm(t, r, orderID, "failed")
	assert.Equal(t, http.StatusConflict, code, "body=%v", body)
	errMsg, _ := body["error"].(string)
	assert.Contains(t, errMsg, "/pay before confirm",
		"error should name the /pay-first contract; got %q", errMsg)

	assert.Zero(t, comp.calls.Load(),
		"Compensator must NOT be called when intent precheck fails")
	assert.Equal(t, "awaiting_payment", readOrderStatus(t, h, orderID),
		"rejected confirm must NOT change state")
}

// missing order → 404, Compensator NOT called.
func TestStageHTTPConfirm_Failed_OrderNotFound(t *testing.T) {
	_, r, comp := stagehttpConfirmHarness(t)
	missing, err := uuid.NewV7()
	require.NoError(t, err)

	code, body := callConfirm(t, r, missing, "failed")
	assert.Equal(t, http.StatusNotFound, code, "body=%v", body)
	assert.Zero(t, comp.calls.Load(),
		"Compensator must NOT be called when the order doesn't exist")
}

// Compensator returns ErrCompensateNotEligible → 409.
//
// Concurrent race scenario: /confirm-failed + a sweeper tick (or
// /confirm-succeeded) hit the same row in parallel. One wins the
// FOR UPDATE; the loser's Compensator returns ErrCompensateNotEligible.
// HandleTestConfirm must NOT log this as an error — it's an
// expected race outcome — and the HTTP response should be 409
// matching MapBookingError's mapping.
func TestStageHTTPConfirm_Failed_CompensatorNotEligible(t *testing.T) {
	h, r, comp := stagehttpConfirmHarness(t)
	comp.returns = stagehttp.ErrCompensateNotEligible

	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), "pi_stage1_existing")

	code, body := callConfirm(t, r, orderID, "failed")
	assert.Equal(t, http.StatusConflict, code, "body=%v", body)
	errMsg, _ := body["error"].(string)
	assert.Contains(t, errMsg, "awaiting_payment",
		"ErrCompensateNotEligible should map to MapBookingError's 409 message; got %q", errMsg)

	assert.EqualValues(t, 1, comp.calls.Load(),
		"Compensator was called (sentinel returned from the call itself)")
}

// Compensator returns a generic error → 500.
//
// Anything that isn't ErrCompensateNotEligible is a real error
// (DB connection lost, driver-state corrupted, etc.). MapBookingError
// catches this in its default branch.
func TestStageHTTPConfirm_Failed_CompensatorGenericError(t *testing.T) {
	h, r, comp := stagehttpConfirmHarness(t)
	comp.returns = errors.New("simulated db connection lost")

	eventID, ttID := seedTicketTypeForPay(t, h, 5)
	orderID := seedOrderRow(t, h, eventID, ttID, 1, "awaiting_payment",
		time.Now().Add(15*time.Minute), "pi_stage1_existing")

	code, body := callConfirm(t, r, orderID, "failed")
	assert.Equal(t, http.StatusInternalServerError, code, "body=%v", body)

	assert.EqualValues(t, 1, comp.calls.Load())
}
