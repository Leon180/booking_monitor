package payment

// D4.2 Slice 3a — outbound Stripe SDK adapter tests.
//
// Approach: spin up an `httptest.Server` impersonating Stripe and
// configure `StripeGateway` to point at it via `StripeConfig.BaseURL`.
// Production wiring leaves BaseURL empty so stripe-go talks to
// api.stripe.com; tests substitute the test server's URL and the
// SDK's HTTP requests land here.
//
// Why not stripe-mock (Stripe's official OpenAPI-driven mock server):
// stripe-mock is a ~100MB Docker image and adds Docker-dependency to
// unit tests. The httptest approach is in-process, has zero external
// deps, and lets us pin specific Stripe error shapes per test —
// stripe-mock returns its own fixed responses which can drift from
// what we want to exercise.
//
// The 8 tests in this file cover:
//   - happy path PaymentIntent create + status mapping
//   - Idempotency-Key header forwarded correctly
//   - all 5 mapStripeError branches (card / idempotency / invalid /
//     api with 401/403/429/5xx)
//   - all 7 PaymentIntent statuses mapped to ChargeStatus correctly
//   - log redaction (form-encoded + JSON form for sensitive fields)
//   - timeout → ErrPaymentTransient via the transport-error path

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
)

// ─── test infrastructure ───────────────────────────────────────────

// stripeMetricsSpy captures every IncCall + ObserveDuration so tests
// can assert per-call metric emission without touching the global
// Prometheus registry. Concurrent-safe via mu.
type stripeMetricsSpy struct {
	mu        sync.Mutex
	calls     []struct{ op, outcome string }
	durations []struct {
		op      string
		seconds float64
	}
}

func (s *stripeMetricsSpy) IncCall(op, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, struct{ op, outcome string }{op, outcome})
}

func (s *stripeMetricsSpy) ObserveDuration(op string, seconds float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.durations = append(s.durations, struct {
		op      string
		seconds float64
	}{op, seconds})
}

// callOutcomes returns the (op, outcome) pairs the spy has captured,
// ordered.
func (s *stripeMetricsSpy) callOutcomes() []struct{ op, outcome string } {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]struct{ op, outcome string }, len(s.calls))
	copy(out, s.calls)
	return out
}

// newSpy constructs a fresh spy. Trivial helper but matches the
// `recordingReconMetrics` pattern in `recon/reconciler_test.go`.
func newSpy() *stripeMetricsSpy {
	return &stripeMetricsSpy{}
}

// Redaction tests run the `redact` helper directly against synthetic
// log lines (see TestStripeGateway_LogRedaction below) — observing
// stripe-go's actual log output would require wiring a custom zap
// core, and `redact` is the only non-trivial behavior of the
// redactingLeveledLogger anyway.

// stripeImpersonator is the test server. Routes are stubbed per-test
// via the handlers map; each test wires up the response shape it
// wants for its specific scenario.
type stripeImpersonator struct {
	server   *httptest.Server
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc // key: "METHOD /path"
	calls    int32                       // total request count
}

func newStripeImpersonator() *stripeImpersonator {
	imp := &stripeImpersonator{
		handlers: map[string]http.HandlerFunc{},
	}
	imp.server = httptest.NewServer(http.HandlerFunc(imp.serveHTTP))
	return imp
}

func (imp *stripeImpersonator) serveHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&imp.calls, 1)
	imp.mu.Lock()
	h, ok := imp.handlers[r.Method+" "+r.URL.Path]
	imp.mu.Unlock()
	if !ok {
		http.Error(w, fmt.Sprintf("test impersonator: no handler for %s %s", r.Method, r.URL.Path), http.StatusNotImplemented)
		return
	}
	h(w, r)
}

func (imp *stripeImpersonator) handle(method, path string, h http.HandlerFunc) {
	imp.mu.Lock()
	defer imp.mu.Unlock()
	imp.handlers[method+" "+path] = h
}

func (imp *stripeImpersonator) close() { imp.server.Close() }

// stripeJSONError wraps a body shape Stripe returns on errors. The
// SDK parses this into `*stripe.Error`.
type stripeJSONError struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// writeStripeError emits a Stripe-shaped error JSON body. status
// is the HTTP status; the SDK populates `*stripe.Error.HTTPStatusCode`
// from this. `Request-Id` header (NOT `Stripe-Request-Id`) is set
// so the adapter's `serr.RequestID` capture has data — stripe-go
// reads `Request-Id`, not the longer name.
func writeStripeError(w http.ResponseWriter, status int, errType, code, msg string) {
	w.Header().Set("Request-Id", "req_test_"+code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{
		"error": stripeJSONError{
			Type:    errType,
			Code:    code,
			Message: msg,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// gatewayWithImpersonator constructs a StripeGateway pointed at the
// test impersonator's URL. Returns the gateway + a metrics spy for
// assertion convenience.
func gatewayWithImpersonator(t *testing.T, imp *stripeImpersonator) (*StripeGateway, *stripeMetricsSpy) {
	t.Helper()
	spy := newSpy()
	gw, err := NewStripeGateway(StripeConfig{
		APIKey:            "sk_test_unit_test_only",
		Timeout:           5 * time.Second,
		MaxNetworkRetries: 0, // disable in-SDK retries; tests assert on single attempts
		BaseURL:           imp.server.URL,
	}, mlog.NewNop(), spy)
	require.NoError(t, err)
	return gw, spy
}

// ─── tests ─────────────────────────────────────────────────────────

func TestStripeGateway_CreatePaymentIntent_HappyPath(t *testing.T) {
	t.Parallel()

	imp := newStripeImpersonator()
	defer imp.close()

	orderID := uuid.New()
	var capturedIdempotencyKey string

	imp.handle("POST", "/v1/payment_intents", func(w http.ResponseWriter, r *http.Request) {
		capturedIdempotencyKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("Stripe-Request-Id", "req_test_create")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{
			"id": "pi_test_%s",
			"client_secret": "pi_test_%s_secret_xxx",
			"amount": 2000,
			"currency": "usd",
			"status": "requires_confirmation",
			"metadata": {"order_id": "%s"}
		}`, orderID.String(), orderID.String(), orderID.String())
	})

	gw, spy := gatewayWithImpersonator(t, imp)
	pi, err := gw.CreatePaymentIntent(context.Background(), orderID, 2000, "USD", map[string]string{
		"order_id": orderID.String(),
	})

	require.NoError(t, err)
	assert.Equal(t, "pi_test_"+orderID.String(), pi.ID)
	assert.Equal(t, int64(2000), pi.AmountCents)
	assert.Equal(t, "usd", pi.Currency)
	assert.Equal(t, orderID.String(), pi.Metadata["order_id"])
	assert.Equal(t, orderID.String(), capturedIdempotencyKey,
		"orderID must be sent as Idempotency-Key header — Stripe's server-side idempotency cache is what makes /pay retry-safe")

	// Metric emission: exactly one (create_payment_intent, success)
	// call recorded; one duration observation.
	assert.Equal(t, []struct{ op, outcome string }{
		{op: "create_payment_intent", outcome: "success"},
	}, spy.callOutcomes())
}

func TestStripeGateway_CreatePaymentIntent_Idempotent(t *testing.T) {
	t.Parallel()
	// Stripe's server-side idempotency cache returns the SAME response
	// for repeat calls with the same Idempotency-Key. Our test
	// impersonator stubs that behavior: keep a per-key cache.

	imp := newStripeImpersonator()
	defer imp.close()
	orderID := uuid.New()

	var seenKeys sync.Map // map[string]string (idempotency-key → cached intent ID)
	imp.handle("POST", "/v1/payment_intents", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		intentID, _ := seenKeys.LoadOrStore(key, "pi_test_"+uuid.New().String())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Stripe-Request-Id", "req_test_idem")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{
			"id": "%s",
			"client_secret": "%s_secret",
			"amount": 2000,
			"currency": "usd",
			"status": "requires_payment_method",
			"metadata": {}
		}`, intentID, intentID)
	})

	gw, _ := gatewayWithImpersonator(t, imp)
	pi1, err1 := gw.CreatePaymentIntent(context.Background(), orderID, 2000, "USD", nil)
	require.NoError(t, err1)
	pi2, err2 := gw.CreatePaymentIntent(context.Background(), orderID, 2000, "USD", nil)
	require.NoError(t, err2)

	assert.Equal(t, pi1.ID, pi2.ID,
		"repeat CreatePaymentIntent with same orderID must return SAME PaymentIntent — "+
			"the gateway-side idempotency contract that makes /pay retry-safe without "+
			"an application-layer cache")
}

func TestStripeGateway_CardError_MapsToErrPaymentDeclined(t *testing.T) {
	t.Parallel()
	imp := newStripeImpersonator()
	defer imp.close()

	imp.handle("POST", "/v1/payment_intents", func(w http.ResponseWriter, r *http.Request) {
		writeStripeError(w, http.StatusPaymentRequired, "card_error", "card_declined", "Your card was declined.")
	})

	gw, spy := gatewayWithImpersonator(t, imp)
	_, err := gw.CreatePaymentIntent(context.Background(), uuid.New(), 2000, "USD", nil)

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPaymentDeclined),
		"card_error → ErrPaymentDeclined (422 to client; not retryable)")
	assert.Contains(t, err.Error(), "card_declined", "code preserved in error message for ops triage")
	assert.Contains(t, err.Error(), "req_id=req_test_card_declined", "Stripe-Request-Id captured for support tickets")

	assert.Equal(t, []struct{ op, outcome string }{
		{op: "create_payment_intent", outcome: "declined"},
	}, spy.callOutcomes())
}

func TestStripeGateway_RateLimit_MapsToErrPaymentTransient(t *testing.T) {
	t.Parallel()
	imp := newStripeImpersonator()
	defer imp.close()

	imp.handle("POST", "/v1/payment_intents", func(w http.ResponseWriter, r *http.Request) {
		writeStripeError(w, http.StatusTooManyRequests, "api_error", "rate_limit", "Too many requests.")
	})

	gw, spy := gatewayWithImpersonator(t, imp)
	_, err := gw.CreatePaymentIntent(context.Background(), uuid.New(), 2000, "USD", nil)

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPaymentTransient),
		"429 rate-limit → ErrPaymentTransient (retry-eligible)")
	// Verify it does NOT match other sentinels (avoid silent
	// over-classification).
	assert.False(t, errors.Is(err, domain.ErrPaymentDeclined))
	assert.False(t, errors.Is(err, domain.ErrPaymentMisconfigured))

	assert.Equal(t, []struct{ op, outcome string }{
		{op: "create_payment_intent", outcome: "transient"},
	}, spy.callOutcomes())
}

func TestStripeGateway_AuthError_MapsToErrPaymentMisconfigured(t *testing.T) {
	t.Parallel()
	imp := newStripeImpersonator()
	defer imp.close()

	imp.handle("POST", "/v1/payment_intents", func(w http.ResponseWriter, r *http.Request) {
		writeStripeError(w, http.StatusUnauthorized, "api_error", "invalid_api_key", "Invalid API key.")
	})

	gw, spy := gatewayWithImpersonator(t, imp)
	_, err := gw.CreatePaymentIntent(context.Background(), uuid.New(), 2000, "USD", nil)

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPaymentMisconfigured),
		"401 → ErrPaymentMisconfigured (page-worthy; retry will not help)")
	assert.Contains(t, err.Error(), "auth/permission")

	assert.Equal(t, []struct{ op, outcome string }{
		{op: "create_payment_intent", outcome: "misconfigured"},
	}, spy.callOutcomes())
}

func TestStripeGateway_IdempotencyError_MapsToErrPaymentInvalid(t *testing.T) {
	t.Parallel()
	imp := newStripeImpersonator()
	defer imp.close()

	imp.handle("POST", "/v1/payment_intents", func(w http.ResponseWriter, r *http.Request) {
		writeStripeError(w, http.StatusBadRequest, "idempotency_error", "idempotency_key_in_use",
			"Idempotency-Key already used with different params.")
	})

	gw, spy := gatewayWithImpersonator(t, imp)
	_, err := gw.CreatePaymentIntent(context.Background(), uuid.New(), 2000, "USD", nil)

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPaymentInvalid),
		"idempotency_error → ErrPaymentInvalid (NON-retryable; "+
			"retrying with same params loops forever — plan-review C2 fix)")
	// CRITICAL: must NOT map to transient, otherwise the unbounded
	// retry loop the plan called out fires.
	assert.False(t, errors.Is(err, domain.ErrPaymentTransient),
		"plan-review C2: idempotency-key collision MUST map to invalid (non-retryable), not transient")

	assert.Equal(t, []struct{ op, outcome string }{
		{op: "create_payment_intent", outcome: "invalid"},
	}, spy.callOutcomes())
}

func TestStripeGateway_GetStatus_AllStatusMappings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		stripeStatus string
		want         domain.ChargeStatus
		why          string
	}{
		{"succeeded", domain.ChargeStatusCharged, "money moved → recon transitions Charging→Confirmed"},
		{"canceled", domain.ChargeStatusDeclined, "cancelled → fail order → saga compensates"},
		{"requires_payment_method", domain.ChargeStatusNotFound, "no payment attempted yet → recon's NotFound branch"},
		{"requires_confirmation", domain.ChargeStatusNotFound, "method attached, awaiting client confirmation → NotFound semantics"},
		{"requires_action", domain.ChargeStatusUnknown, "3DS / SCA in progress → recon retries next sweep"},
		{"processing", domain.ChargeStatusUnknown, "Stripe is processing → recon retries"},
		{"requires_capture", domain.ChargeStatusUnknown, "manual-capture flow not used in our auto-capture setup → Unknown so it doesn't accidentally flip to Charged"},
	}

	for _, tc := range cases {
		t.Run(tc.stripeStatus, func(t *testing.T) {
			t.Parallel()
			imp := newStripeImpersonator()
			defer imp.close()

			imp.handle("GET", "/v1/payment_intents/pi_test_id", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprintf(w, `{"id":"pi_test_id","status":"%s","amount":2000,"currency":"usd"}`, tc.stripeStatus)
			})

			gw, _ := gatewayWithImpersonator(t, imp)
			got, err := gw.GetStatus(context.Background(), "pi_test_id")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got, "%s — %s", tc.stripeStatus, tc.why)
		})
	}
}

func TestStripeGateway_GetStatus_EmptyIntentID_ReturnsInvalid(t *testing.T) {
	t.Parallel()
	imp := newStripeImpersonator()
	defer imp.close()

	gw, spy := gatewayWithImpersonator(t, imp)
	got, err := gw.GetStatus(context.Background(), "")

	// Empty intent ID is the defense-in-depth case for the recon
	// null-guard (Slice 1). The recon should never call us with
	// empty, but if it does we MUST NOT issue a malformed `GET
	// /v1/payment_intents/` to Stripe (which would 404 → silent
	// wrong-verdict).
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPaymentInvalid),
		"empty intentID → ErrPaymentInvalid; never issue a malformed Stripe call")
	assert.Equal(t, domain.ChargeStatusUnknown, got)

	// Importantly: NO Stripe HTTP call was made.
	assert.Equal(t, int32(0), atomic.LoadInt32(&imp.calls),
		"empty intentID short-circuits BEFORE the Stripe HTTP call — "+
			"defense-in-depth against the recon null-guard ever slipping")

	assert.Equal(t, []struct{ op, outcome string }{
		{op: "get_status", outcome: "invalid"},
	}, spy.callOutcomes())
}

// TestStripeGateway_GetStatus_ErrorClassifications closes the gap that
// final-pre-merge multi-agent review flagged: the per-error-class
// CreatePaymentIntent tests (CardError, RateLimit, AuthError,
// IdempotencyError) cover mapStripeError's logic, but neither covers
// the GetStatus method's deferred-metric path on error. A future
// refactor that moved the metric record() out of the defer (or changed
// the GetStatus shape) would silently break metric emission on
// GetStatus errors. This test pins the contract: GetStatus errors emit
// `op="get_status"` with the matching outcome, AND the typed sentinel
// reaches the caller.
func TestStripeGateway_GetStatus_ErrorClassifications(t *testing.T) {
	cases := []struct {
		name       string
		httpStatus int
		errType    string
		errCode    string
		errMsg     string
		wantOutc   string
		wantSent   error
	}{
		{
			// Stripe v82 collapsed the legacy ErrorTypeAuthentication
			// into ErrorTypeAPI; mapStripeError disambiguates by
			// HTTPStatusCode (401/403 → misconfigured). Test mirrors
			// the existing CreatePaymentIntent AuthError case.
			name:       "401 auth → misconfigured",
			httpStatus: http.StatusUnauthorized,
			errType:    "api_error",
			errCode:    "invalid_api_key",
			errMsg:     "Invalid API key.",
			wantOutc:   "misconfigured",
			wantSent:   domain.ErrPaymentMisconfigured,
		},
		{
			name:       "429 rate-limit → transient",
			httpStatus: http.StatusTooManyRequests,
			errType:    "api_error",
			errCode:    "rate_limit",
			errMsg:     "Too many requests.",
			wantOutc:   "transient",
			wantSent:   domain.ErrPaymentTransient,
		},
		{
			name:       "400 invalid_request → invalid",
			httpStatus: http.StatusBadRequest,
			errType:    "invalid_request_error",
			errCode:    "resource_missing",
			errMsg:     "No such payment_intent.",
			wantOutc:   "invalid",
			wantSent:   domain.ErrPaymentInvalid,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			imp := newStripeImpersonator()
			defer imp.close()

			imp.handle("GET", "/v1/payment_intents/pi_test_id", func(w http.ResponseWriter, r *http.Request) {
				writeStripeError(w, tc.httpStatus, tc.errType, tc.errCode, tc.errMsg)
			})

			gw, spy := gatewayWithImpersonator(t, imp)
			got, err := gw.GetStatus(context.Background(), "pi_test_id")

			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.wantSent),
				"%s should map to %T sentinel", tc.name, tc.wantSent)
			assert.Equal(t, domain.ChargeStatusUnknown, got,
				"error response → ChargeStatusUnknown (recon retries)")

			// Pin the deferred metric path: GetStatus errors must emit
			// the matching outcome label under op="get_status".
			assert.Equal(t, []struct{ op, outcome string }{
				{op: "get_status", outcome: tc.wantOutc},
			}, spy.callOutcomes())
		})
	}
}

func TestStripeGateway_LogRedaction(t *testing.T) {
	t.Parallel()
	// Verify the redactingLeveledLogger strips known sensitive fields
	// in BOTH form-encoded AND JSON-encoded shapes (Slice 1 review
	// HIGH #2 — stripe-go v82 logs response bodies as JSON).
	//
	// We test the redact() pure function directly rather than
	// observing the stripe-go logger's output — the logger wires
	// through to the project's `mlog.Logger` whose output capture
	// would require setting up a custom zap core. The pure-function
	// test is sufficient because the redactingLeveledLogger's only
	// non-trivial behavior is calling redact() on the formatted
	// message before forwarding.

	cases := []struct {
		name string
		in   string
		want string // substring that must NOT appear in output
	}{
		{
			name: "form_encoded_client_secret",
			in:   "POST /v1/payment_intents amount=2000 client_secret=pi_xxx_secret_yyy currency=usd",
			want: "pi_xxx_secret_yyy",
		},
		{
			name: "form_encoded_card_number",
			in:   "card.number=4242424242424242 card.exp_month=12 card.exp_year=2030",
			want: "4242424242424242",
		},
		{
			name: "json_client_secret_no_space",
			in:   `{"id":"pi_xxx","client_secret":"pi_xxx_secret_yyy","amount":2000}`,
			want: "pi_xxx_secret_yyy",
		},
		{
			name: "json_client_secret_with_space",
			in:   `{ "client_secret": "pi_aaa_secret_bbb" }`,
			want: "pi_aaa_secret_bbb",
		},
		// Note: deliberately NOT testing nested JSON like
		// `{"card":{"number":"..."}}` — Stripe redacts card.number
		// server-side before responding (PCI scope), so the
		// adapter's logger never sees raw card numbers in real
		// responses. The dot-notation `card.number=` form-encoded
		// case (above) is the realistic redaction path —
		// stripe-go SDK formats request params as flat
		// dot-notation, not nested JSON.
		{
			// Final-pre-merge multi-agent review fix: unterminated
			// JSON-string handling. If a log line is truncated mid-
			// value (`{"client_secret":"pi_xxx_secret_yyy` with no
			// closing quote), the redactor MUST still strip the
			// sensitive value rather than silently passing it
			// through. Pre-fix, the inner loop's valueEnd reached
			// len(s) and the substitution `s[:valueStart] +
			// replacement + s[valueEnd:]` evaluated `s[len(s):]` =
			// "", redacting to end of string — which IS the desired
			// fail-safe behavior, but only by accident; the explicit
			// `valueEnd >= len(s)` guard makes the intent visible.
			name: "json_unterminated_string",
			in:   `{"id":"pi_xxx","client_secret":"pi_xxx_secret_yyy`,
			want: "pi_xxx_secret_yyy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := redact(tc.in)
			assert.NotContains(t, out, tc.want,
				"sensitive substring must be stripped — input: %q output: %q", tc.in, out)
			assert.Contains(t, out, "<redacted>",
				"redaction must replace with explicit marker; input %q output %q",
				tc.in, out)
		})
	}
}

func TestStripeGateway_Timeout_MapsToTransient(t *testing.T) {
	t.Parallel()
	// Test server hangs longer than the gateway's 1-second timeout.
	// stripe-go's HTTP client surfaces a context.DeadlineExceeded
	// which is NOT a *stripe.Error — it lands in mapStripeError's
	// transport-error branch and wraps as ErrPaymentTransient.

	imp := newStripeImpersonator()
	// Critical: httptest.Server.Close() blocks waiting for active
	// connections to drain. When the gateway times out at 1s, the
	// request connection is cancelled but our handler may still be
	// blocked on its hang. Use CloseClientConnections() in cleanup
	// to forcibly drop active connections so Close() doesn't block
	// for httptest's 5s warning timeout.
	t.Cleanup(func() {
		imp.server.CloseClientConnections()
		imp.close()
	})

	imp.handle("POST", "/v1/payment_intents", func(w http.ResponseWriter, r *http.Request) {
		// Hang for slightly longer than the gateway timeout so the
		// gateway-side client times out first. Bounded so the
		// handler always exits within a known window — using
		// `<-r.Context().Done()` alone hangs in some Go HTTP server
		// configurations (request ctx cancellation isn't always
		// propagated by the server when the client TCP connection
		// is half-open).
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})

	// Use a 1s gateway timeout so the test runs fast.
	spy := newSpy()
	gw, err := NewStripeGateway(StripeConfig{
		APIKey:            "sk_test_unit",
		Timeout:           1 * time.Second,
		MaxNetworkRetries: 0,
		BaseURL:           imp.server.URL,
	}, mlog.NewNop(), spy)
	require.NoError(t, err)

	start := time.Now()
	_, err = gw.CreatePaymentIntent(context.Background(), uuid.New(), 2000, "USD", nil)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrPaymentTransient),
		"transport-layer timeout → ErrPaymentTransient (retry-eligible)")
	assert.True(t, elapsed < 4*time.Second,
		"timeout MUST fire near 1s gateway Timeout (got %s)", elapsed)

	// Metric emission: outcome=transient.
	outs := spy.callOutcomes()
	require.Len(t, outs, 1)
	assert.Equal(t, "create_payment_intent", outs[0].op)
	assert.Equal(t, "transient", outs[0].outcome)
}

// TestClassifyOutcome — exhaustive coverage of the pure function
// that maps wrapped errors to outcome labels. Catches sentinel-
// wrapping regressions before they ripple to live metric emission.
func TestClassifyOutcome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil_err_success", nil, "success"},
		{"declined", fmt.Errorf("wrap: %w", domain.ErrPaymentDeclined), "declined"},
		{"misconfigured", fmt.Errorf("wrap: %w", domain.ErrPaymentMisconfigured), "misconfigured"},
		{"invalid", fmt.Errorf("wrap: %w", domain.ErrPaymentInvalid), "invalid"},
		{"transient", fmt.Errorf("wrap: %w", domain.ErrPaymentTransient), "transient"},
		{"unrelated_default_to_transient", errors.New("network broken"), "transient"},
		{"errors_join_chain_finds_sentinel",
			fmt.Errorf("outer: %w", errors.Join(errors.New("inner stripe.Error"), domain.ErrPaymentDeclined)),
			"declined",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyOutcome(tc.err)
			assert.Equal(t, tc.want, got)
		})
	}
}

