package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"booking_monitor/internal/log"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    log.Level
		wantErr bool
	}{
		{"debug", log.DebugLevel, false},
		{"info", log.InfoLevel, false},
		{"warn", log.WarnLevel, false},
		{"error", log.ErrorLevel, false},
		{"fatal", log.FatalLevel, false},
		{"", 0, true},
		{"wanr", 0, true}, // typo — must not silently fall back
		{"verbose", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := log.ParseLevel(tc.in)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNew_WritesJSON(t *testing.T) {
	var buf bytes.Buffer
	l, err := log.New(log.Options{Level: log.InfoLevel, Output: &buf})
	require.NoError(t, err)

	l.L().Info("hello", zap.String("who", "world"))
	require.NoError(t, l.Sync())

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "hello", entry["msg"])
	assert.Equal(t, "info", entry["level"])
	assert.Equal(t, "world", entry["who"])
	assert.NotEmpty(t, entry["time"])
}

func TestNew_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	l, err := log.New(log.Options{Level: log.WarnLevel, Output: &buf})
	require.NoError(t, err)

	l.L().Info("should be dropped")
	l.L().Warn("should appear")
	require.NoError(t, l.Sync())

	got := buf.String()
	assert.NotContains(t, got, "should be dropped")
	assert.Contains(t, got, "should appear")
}

func TestLogger_With_ReturnsNew(t *testing.T) {
	var buf bytes.Buffer
	base, err := log.New(log.Options{Level: log.InfoLevel, Output: &buf})
	require.NoError(t, err)

	child := base.With(zap.String("req", "abc"))
	assert.NotSame(t, base, child, "With must return a new Logger")

	child.L().Info("hi")
	require.NoError(t, child.Sync())

	// child emits the bound field
	assert.Contains(t, buf.String(), `"req":"abc"`)
}

func TestLogger_With_SharesAtomicLevel(t *testing.T) {
	base, err := log.New(log.Options{Level: log.InfoLevel, Output: new(bytes.Buffer)})
	require.NoError(t, err)

	child := base.With(zap.String("k", "v"))

	// changing the level on base must affect child
	base.Level().SetLevel(log.ErrorLevel)
	assert.Equal(t, log.ErrorLevel, child.Level().Level())
}

func TestLogger_Level_ChangesAtRuntime(t *testing.T) {
	var buf bytes.Buffer
	l, err := log.New(log.Options{Level: log.InfoLevel, Output: &buf})
	require.NoError(t, err)

	l.L().Debug("first debug — dropped")

	l.Level().SetLevel(log.DebugLevel)
	l.L().Debug("second debug — kept")
	require.NoError(t, l.Sync())

	got := buf.String()
	assert.NotContains(t, got, "first debug")
	assert.Contains(t, got, "second debug")
}

func TestContext_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l, err := log.New(log.Options{Level: log.InfoLevel, Output: &buf})
	require.NoError(t, err)

	ctx := log.NewContext(context.Background(), l, "")
	got := log.FromContext(ctx)
	assert.Same(t, l, got)
}

func TestContext_FallsBackToNop(t *testing.T) {
	// No logger in context → Nop (silent) — verify by writing to it and
	// checking nothing came out.
	l := log.FromContext(context.Background())
	require.NotNil(t, l)
	// Nop's writes go to io.Discard, so there's nothing observable.
	// We at least assert it doesn't panic.
	l.L().Info("this should vanish")
	require.NoError(t, l.Sync())
}

func TestContext_NilContext(t *testing.T) {
	// Passing a nil context must not panic.
	l := log.FromContext(nil)
	require.NotNil(t, l)
}

func TestNewNop_IsSilent(t *testing.T) {
	l := log.NewNop()
	// Even turning the level down doesn't produce output (internal
	// zap.NewNop ignores everything).
	l.Level().SetLevel(log.DebugLevel)
	l.L().Debug("should not appear anywhere")
	l.L().Error("not this either")
	require.NoError(t, l.Sync())
}

func TestLevelHandler_Get(t *testing.T) {
	l, err := log.New(log.Options{Level: log.WarnLevel, Output: new(bytes.Buffer)})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/admin/loglevel", nil)
	rr := httptest.NewRecorder()
	l.LevelHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "warn", body["level"])
}

func TestLevelHandler_PostForm(t *testing.T) {
	l, err := log.New(log.Options{Level: log.InfoLevel, Output: new(bytes.Buffer)})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/loglevel", strings.NewReader("level=debug"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	l.LevelHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, log.DebugLevel, l.Level().Level())
}

func TestLevelHandler_PostJSON(t *testing.T) {
	l, err := log.New(log.Options{Level: log.InfoLevel, Output: new(bytes.Buffer)})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/loglevel", strings.NewReader(`{"level":"error"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	l.LevelHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, log.ErrorLevel, l.Level().Level())
}

func TestLevelHandler_InvalidLevel(t *testing.T) {
	l, err := log.New(log.Options{Level: log.InfoLevel, Output: new(bytes.Buffer)})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/loglevel", strings.NewReader("level=verbose"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	l.LevelHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	// Level must stay unchanged.
	assert.Equal(t, log.InfoLevel, l.Level().Level())
}

func TestLevelHandler_MissingLevel(t *testing.T) {
	l, err := log.New(log.Options{Level: log.InfoLevel, Output: new(bytes.Buffer)})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/loglevel", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	l.LevelHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestLevelHandler_MethodNotAllowed(t *testing.T) {
	l, err := log.New(log.Options{Level: log.InfoLevel, Output: new(bytes.Buffer)})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/admin/loglevel", nil)
	rr := httptest.NewRecorder()
	l.LevelHandler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.NotEmpty(t, rr.Header().Get("Allow"))
}
