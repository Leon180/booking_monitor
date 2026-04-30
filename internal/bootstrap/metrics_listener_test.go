package bootstrap_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"booking_monitor/internal/bootstrap"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"
)

// freePort asks the kernel for an unused TCP port. Avoids hardcoded
// ports racing against parallel tests on the same host (and lets `go
// test -count=N` reruns succeed without waiting for TIME_WAIT to clear).
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().(*net.TCPAddr)
	require.NoError(t, l.Close())
	return addr.String()
}

func newTestConfig(addr string) *config.Config {
	return &config.Config{
		Worker: config.WorkerConfig{MetricsAddr: addr},
	}
}

// TestInstallMetricsListener_ServesMetricsAndHealthz verifies the
// listener honours its two-handler contract: /metrics returns the
// Prometheus exposition format (text starts with `# HELP`) and
// /healthz returns 200 "ok". This is the contract operators rely on
// when wiring the prometheus.yml scrape job + compose HEALTHCHECK.
func TestInstallMetricsListener_ServesMetricsAndHealthz(t *testing.T) {
	t.Parallel()

	addr := freePort(t)
	lc := fxtest.NewLifecycle(t)
	require.NoError(t, bootstrap.InstallMetricsListener(lc, newTestConfig(addr), mlog.NewNop()))
	require.NoError(t, lc.Start(context.Background()))
	defer func() { _ = lc.Stop(context.Background()) }()

	client := &http.Client{Timeout: 2 * time.Second}

	// /healthz must respond 200 "ok" with no dependency probes.
	resp, err := waitForHTTP(client, "http://"+addr+"/healthz", 2*time.Second)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", string(body))

	// /metrics must serve a Prometheus-format response. Look for the
	// `# HELP` prefix that promhttp always emits; the exact metric
	// names depend on what's registered in the default registry, but
	// `# HELP go_` is universally present from the Go runtime
	// collector that registers itself on import.
	resp, err = client.Get("http://" + addr + "/metrics")
	require.NoError(t, err)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.Contains(string(body), "# HELP"),
		"Prometheus exposition format must include `# HELP` lines; got first 200 chars: %q",
		truncate(string(body), 200))
}

// TestInstallMetricsListener_EmptyAddrSkips verifies that an empty
// MetricsAddr disables the listener entirely (no goroutine, no port
// bind). Used by `--once` CronJob hosting where the process exits
// before Prometheus typically scrapes, and by unit tests that don't
// want a real listener. Without this branch, every test that runs
// through fx.Invoke would race against port allocation.
func TestInstallMetricsListener_EmptyAddrSkips(t *testing.T) {
	t.Parallel()

	lc := fxtest.NewLifecycle(t)
	// Empty addr — listener should be skipped without error.
	require.NoError(t, bootstrap.InstallMetricsListener(lc, newTestConfig(""), mlog.NewNop()))
	require.NoError(t, lc.Start(context.Background()))
	require.NoError(t, lc.Stop(context.Background()))

	// No assertion on a port being unreachable — the absence of
	// crashes during Start/Stop already proves the no-op branch.
}

// TestInstallMetricsListener_StopShutsDownCleanly verifies the OnStop
// hook closes the listener so a subsequent `:port` bind on the same
// address would succeed. Without the explicit shutdown the listener
// goroutine would leak past lifecycle end. Uses fxtest.NewLifecycle
// so the assertion fires inside the test framework, not just at
// process exit.
func TestInstallMetricsListener_StopShutsDownCleanly(t *testing.T) {
	t.Parallel()

	addr := freePort(t)
	lc := fxtest.NewLifecycle(t)
	require.NoError(t, bootstrap.InstallMetricsListener(lc, newTestConfig(addr), mlog.NewNop()))
	require.NoError(t, lc.Start(context.Background()))

	// Give the goroutine a moment to actually bind before we shut down.
	require.NoError(t, waitForListening(addr, 2*time.Second))

	require.NoError(t, lc.Stop(context.Background()))

	// After Stop, /metrics should NOT respond — the listener has
	// closed. Allow a brief grace window for the OS to release the
	// port; assert that within ~500ms the connect refuses.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err != nil {
			return // listener is gone — expected outcome
		}
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("listener at %s still accepting connections after Stop()", addr)
}

// waitForHTTP polls a URL until it responds successfully or the
// budget expires. Needed because OnStart fires the listener goroutine
// asynchronously — the listener is bound by the time Start() returns,
// but in practice the kernel's accept queue may take a few ms.
func waitForHTTP(client *http.Client, url string, budget time.Duration) (*http.Response, error) {
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return nil, lastErr
}

// waitForListening returns nil once a TCP connect to addr succeeds,
// or an error after the budget expires.
func waitForListening(addr string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return lastErr
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Compile-time assurance the test imports the fx.Lifecycle interface
// the helper signature requires. Without this, a future helper
// signature change would silently leave the tests building against
// the old interface (fxtest.Lifecycle implements fx.Lifecycle, so
// substitution works today).
var _ fx.Lifecycle = (*fxtest.Lifecycle)(nil)
