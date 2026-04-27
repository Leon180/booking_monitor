package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/infrastructure/api"
)

func init() { gin.SetMode(gin.TestMode) }

func TestLive_AlwaysOK(t *testing.T) {
	t.Parallel()

	h := api.NewHealthHandlerForTest(nil, nil, 0)

	r := gin.New()
	api.RegisterHealthRoutes(r, h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestReady_AllOK(t *testing.T) {
	t.Parallel()

	ok := func(_ context.Context) error { return nil }
	h := api.NewHealthHandlerForTest(
		[]string{"postgres", "redis", "kafka"},
		[]func(context.Context) error{ok, ok, ok},
		0,
	)

	w := doReady(t, h)
	assert.Equal(t, http.StatusOK, w.Code)

	body := decodeReady(t, w)
	assert.Equal(t, "ok", body.Status)
	require.Len(t, body.Checks, 3)
	for _, c := range body.Checks {
		assert.True(t, c.OK, "%s should be OK", c.Name)
		assert.Empty(t, c.Error)
	}
}

func TestReady_OneFailureReturns503(t *testing.T) {
	t.Parallel()

	ok := func(_ context.Context) error { return nil }
	boom := func(_ context.Context) error { return errors.New("connection refused") }

	h := api.NewHealthHandlerForTest(
		[]string{"postgres", "redis", "kafka"},
		[]func(context.Context) error{ok, boom, ok},
		0,
	)

	w := doReady(t, h)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	body := decodeReady(t, w)
	assert.Equal(t, "unavailable", body.Status)
	require.Len(t, body.Checks, 3)
	assert.True(t, body.Checks[0].OK)
	assert.False(t, body.Checks[1].OK)
	assert.Equal(t, "connection refused", body.Checks[1].Error)
	assert.True(t, body.Checks[2].OK)
}

func TestReady_OrderIsDeterministic(t *testing.T) {
	t.Parallel()

	// One check sleeps long enough that, if results were appended in
	// completion order, "postgres" would land last. The pre-allocated
	// index-write pattern in runChecks must keep registration order
	// regardless of which goroutine finishes first — that's what makes
	// the JSON body stable for dashboards / alerts.
	slow := func(_ context.Context) error { time.Sleep(20 * time.Millisecond); return nil }
	fast := func(_ context.Context) error { return nil }
	mid := func(_ context.Context) error { return nil }

	h := api.NewHealthHandlerForTest(
		[]string{"postgres", "redis", "kafka"},
		[]func(context.Context) error{slow, fast, mid},
		0,
	)

	w := doReady(t, h)
	body := decodeReady(t, w)
	require.Len(t, body.Checks, 3)
	assert.Equal(t, "postgres", body.Checks[0].Name)
	assert.Equal(t, "redis", body.Checks[1].Name)
	assert.Equal(t, "kafka", body.Checks[2].Name)
}

func TestReady_RespectsBudget(t *testing.T) {
	t.Parallel()

	// A check that blocks indefinitely until ctx is cancelled; the
	// readiness handler must time out via readinessProbeBudget and
	// surface that as a check failure rather than hanging.
	hang := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ok := func(_ context.Context) error { return nil }

	// 50ms budget keeps the test sub-100ms instead of burning the
	// 1s production default. Override is the whole reason
	// probeBudget is a HealthHandler field rather than a const.
	h := api.NewHealthHandlerForTest(
		[]string{"postgres", "redis"},
		[]func(context.Context) error{hang, ok},
		50*time.Millisecond,
	)

	w := doReady(t, h)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	body := decodeReady(t, w)
	require.Len(t, body.Checks, 2)
	assert.False(t, body.Checks[0].OK)
	assert.True(t, strings.Contains(body.Checks[0].Error, "deadline exceeded") ||
		strings.Contains(body.Checks[0].Error, "context canceled"),
		"expected ctx error, got %q", body.Checks[0].Error)
	assert.True(t, body.Checks[1].OK)
}

// readyResponse mirrors the JSON body /readyz writes. Kept here (test
// only) so changes to the handler's response shape force a test edit.
type readyResponse struct {
	Status string `json:"status"`
	Checks []struct {
		Name  string `json:"name"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	} `json:"checks"`
}

func doReady(t *testing.T, h *api.HealthHandler) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	api.RegisterHealthRoutes(r, h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	r.ServeHTTP(w, req)
	return w
}

func decodeReady(t *testing.T, w *httptest.ResponseRecorder) readyResponse {
	t.Helper()
	var body readyResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}
