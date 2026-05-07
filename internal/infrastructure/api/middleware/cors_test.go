package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/infrastructure/api/middleware"
)

// fixture builds a minimal gin engine with the CORS middleware mounted
// + a single GET/POST/OPTIONS-accepting endpoint at /probe. Tests
// drive HTTP recorders against it; no real network.
func fixture(t *testing.T, allowed []string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.CORS(allowed))
	// Generic handler used by GET and POST; OPTIONS is intercepted by
	// the middleware via AbortWithStatus(204) before reaching here.
	probe := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.GET("/probe", probe)
	r.POST("/probe", probe)
	return r
}

func do(t *testing.T, r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// ── allow-list disabled (empty) — production-safe no-op ──────────────

func TestCORS_EmptyAllowList_NoOp(t *testing.T) {
	r := fixture(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := do(t, r, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"),
		"empty allow-list MUST NOT echo Allow-Origin (production-safe default)")
	assert.Empty(t, rec.Header().Values("Vary"),
		"empty allow-list disables the middleware entirely — no Vary either")
}

// ── Vary: Origin on every response (cache-correctness invariant) ─────

// Vary: Origin must fire on EVERY response (matched origin, unmatched
// origin, OPTIONS, regular GET, no-Origin) so intermediate proxies
// don't cross-pollute one origin's response onto another. ACRM/ACRH
// Vary entries are scoped to OPTIONS only and tested separately —
// see TestCORS_VaryHeader_PreflightAddsACRMACRH.
func TestCORS_VaryOrigin_EveryResponse(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173"})

	cases := []struct {
		name   string
		method string
		origin string
	}{
		{"GET matched origin", http.MethodGet, "http://localhost:5173"},
		{"GET unmatched origin", http.MethodGet, "http://evil.example.com"},
		{"GET no origin (same-origin)", http.MethodGet, ""},
		{"POST matched origin", http.MethodPost, "http://localhost:5173"},
		{"OPTIONS preflight matched origin", http.MethodOptions, "http://localhost:5173"},
		{"OPTIONS preflight unmatched origin", http.MethodOptions, "http://evil.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/probe", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", "POST")
				req.Header.Set("Access-Control-Request-Headers", "content-type")
			}
			rec := do(t, r, req)

			vary := rec.Header().Values("Vary")
			joined := strings.Join(vary, ", ")
			assert.Contains(t, joined, "Origin",
				"Vary MUST list Origin on every response (cache-correctness invariant)")
		})
	}
}

// Preflight responses additionally Vary on ACRM + ACRH because those
// are the preflight cache-key axes — a proxy that caches the 204
// must not reuse it across requests with different method/headers
// asks. Non-OPTIONS responses do NOT add these (RFC 7231 §7.1.4 —
// Vary should list only headers that actually affected the response).
func TestCORS_VaryHeader_PreflightAddsACRMACRH(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173"})

	t.Run("OPTIONS matched origin: Vary lists Origin + ACRM + ACRH", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type")
		rec := do(t, r, req)

		joined := strings.Join(rec.Header().Values("Vary"), ", ")
		assert.Contains(t, joined, "Origin")
		assert.Contains(t, joined, "Access-Control-Request-Method",
			"preflight MUST Vary on ACRM (preflight cache key)")
		assert.Contains(t, joined, "Access-Control-Request-Headers",
			"preflight MUST Vary on ACRH (preflight cache key)")
	})

	t.Run("GET matched origin: Vary lists Origin only — no ACRM/ACRH noise", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		rec := do(t, r, req)

		joined := strings.Join(rec.Header().Values("Vary"), ", ")
		assert.Contains(t, joined, "Origin")
		assert.NotContains(t, joined, "Access-Control-Request-Method",
			"non-preflight MUST NOT Vary on ACRM — RFC 7231 §7.1.4 (header didn't affect response)")
		assert.NotContains(t, joined, "Access-Control-Request-Headers",
			"non-preflight MUST NOT Vary on ACRH — RFC 7231 §7.1.4 (header didn't affect response)")
	})
}

// ── allow-list match: Allow-Origin echoed ────────────────────────────

func TestCORS_AllowedOrigin_GET_EchoesOrigin(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173", "http://127.0.0.1:5173"})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := do(t, r, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"),
		"matched origin must be echoed verbatim (single value, not *)")
}

func TestCORS_AllowedOrigin_127001Matches(t *testing.T) {
	// Round-2 P3 fix: localhost and 127.0.0.1 are different origins
	// to the browser. Both must be supported simultaneously when
	// listed in the allow-list.
	r := fixture(t, []string{"http://localhost:5173", "http://127.0.0.1:5173"})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	rec := do(t, r, req)

	assert.Equal(t, "http://127.0.0.1:5173", rec.Header().Get("Access-Control-Allow-Origin"))
}

// ── allow-list miss: no Allow-Origin (don't 403) ─────────────────────

func TestCORS_DisallowedOrigin_NoAllowOrigin_NoError(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173"})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := do(t, r, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"unmatched origin MUST NOT 403 — leaking allow-list shape to a scanner is worse than letting the browser reject the response itself")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"),
		"unmatched origin: no Allow-Origin header → browser rejects per CORS spec")
}

// ── preflight: OPTIONS short-circuits with 204 + headers ─────────────

// Round-1 P1 fix: preflight for `Content-Type: application/json` +
// `Idempotency-Key` MUST succeed. These headers make every demo POST
// non-simple, triggering preflight. If they're not in the allow list,
// `curl smoke` passes but the browser is blocked.
func TestCORS_Preflight_AllowsContentTypeAndIdempotencyKey(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173"})

	req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,idempotency-key")
	rec := do(t, r, req)

	assert.Equal(t, http.StatusNoContent, rec.Code, "preflight MUST short-circuit with 204")
	assert.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "OPTIONS")
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	assert.Contains(t, allowHeaders, "Content-Type", "Content-Type MUST be allowed")
	assert.Contains(t, allowHeaders, "Idempotency-Key", "Idempotency-Key MUST be allowed for POST /book")
	assert.Equal(t, "600", rec.Header().Get("Access-Control-Max-Age"))
	assert.Equal(t, "", rec.Body.String(), "204 response MUST have empty body")
}

// Round-1 P1 fix: header-name comparison is case-insensitive per
// RFC 7230 §3.2. Browsers lowercase header names in
// Access-Control-Request-Headers; mixed-case must also work because
// proxies / fetch implementations may not lowercase consistently.
func TestCORS_Preflight_MixedCaseHeaders(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173"})

	req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	// Mix of casing — middleware must echo a static allow-headers list
	// regardless of the requested-headers casing (the browser then
	// case-insensitively compares against the response's allow-list).
	req.Header.Set("Access-Control-Request-Headers", "Content-Type,Idempotency-Key,X-Correlation-ID")
	rec := do(t, r, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	allowHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	// We emit the headers in the casing operators expect to read in
	// docs / runbooks; the browser handles case-insensitive matching.
	assert.Contains(t, allowHeaders, "Content-Type")
	assert.Contains(t, allowHeaders, "Idempotency-Key")
	assert.Contains(t, allowHeaders, "X-Correlation-ID")
}

// ── preflight: OPTIONS for unmatched origin → no Allow-Origin ───────

func TestCORS_Preflight_UnmatchedOrigin_NoAllowHeaders(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173"})

	req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := do(t, r, req)

	// gin's default OPTIONS routing returns 404 because we registered
	// only GET + POST on /probe. Because the origin is unmatched the
	// CORS middleware doesn't intercept (it only short-circuits for
	// matched origins). Either 404 or 200 is fine for the test —
	// the assertion that matters is "no allow-origin / allow-headers
	// were echoed."
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"),
		"unmatched origin preflight MUST NOT echo Allow-Origin")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Headers"),
		"unmatched origin preflight MUST NOT echo Allow-Headers")
}

// ── allow-list trims whitespace + ignores empty entries ──────────────

func TestCORS_AllowList_TrimsAndIgnoresEmpty(t *testing.T) {
	// Defensive: env-CSV parsing can produce trailing whitespace +
	// stray empty entries (e.g. "http://localhost:5173, ,").
	r := fixture(t, []string{"  http://localhost:5173  ", "", "   "})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := do(t, r, req)

	assert.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"),
		"whitespace in allow-list entries must be trimmed")
}

func TestCORS_AllowList_AllEmptyEntries_BehaveLikeDisabled(t *testing.T) {
	r := fixture(t, []string{"", "  "})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := do(t, r, req)

	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, rec.Header().Values("Vary"),
		"all-empty allow-list normalises to disabled — no Vary either")
}

// ── no Origin header (same-origin or non-browser caller) ─────────────

func TestCORS_NoOriginHeader_VaryStillEmitted(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173"})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	rec := do(t, r, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"),
		"no Origin = no CORS echo")
	// Vary is still emitted — the middleware sets it before the Origin check.
	assert.NotEmpty(t, rec.Header().Values("Vary"),
		"Vary headers are emitted regardless of Origin presence (cache correctness)")
}

// ── ensure middleware runs without panic when caller's request has
//    a malformed Origin (defensive smoke) ─────────────────────────────

func TestCORS_MalformedOrigin_DoesNotPanic(t *testing.T) {
	r := fixture(t, []string{"http://localhost:5173"})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "not-a-valid-origin")
	require.NotPanics(t, func() {
		do(t, r, req)
	})
}
