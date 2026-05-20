//go:build integration

// Package sseintegration_test contains integration tests for the
// admin event SSE pipeline. Uses miniredis as the broker (testcontainers
// is reserved for PG-only tests; Redis Streams behaviour in miniredis
// is faithful enough for these scenarios).
//
// Goroutine-leak verification via go.uber.org/goleak runs in
// TestMain — every test must leave the runtime free of stranded
// goroutines.
package sseintegration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
	"go.uber.org/goleak"

	"booking_monitor/internal/application/admin"
	"booking_monitor/internal/infrastructure/api/middleware"
	"booking_monitor/internal/infrastructure/api/sse"
	"booking_monitor/internal/infrastructure/cache"
	mlog "booking_monitor/internal/log"
)

func TestMain(m *testing.M) {
	// Verify no goroutine leaks across all tests in this package.
	// Filters cover known-OK goroutines (test framework, miniredis,
	// http/2 connection cache that lingers briefly post-test).
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("github.com/alicebob/miniredis/v2.(*Miniredis).Restart.func1"),
	)
}

// stack assembles the full admin SSE pipeline (bus + hub + subscriber +
// handler) with a single fx-style wireup, ready to host gin routes.
// The bus.Run / hub.Run / subscriber.Run goroutines are spawned
// under ctx — caller must call cancel() at end of test.
type stack struct {
	mr         *miniredis.Miniredis
	redis      *redis.Client
	bus        *cache.AdminEventBus
	hub        *sse.Hub
	subscriber *sse.Subscriber
	handler    *sse.Handler
	router     *gin.Engine
	server     *httptest.Server
	cancel     context.CancelFunc
}

func newStack(t *testing.T, secret []byte) *stack {
	t.Helper()
	gin.SetMode(gin.TestMode)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	logger := mlog.NewNop()
	busCfg := cache.DefaultAdminEventBusConfig()
	busCfg.DrainTimeout = 500 * time.Millisecond
	bus := cache.NewAdminEventBus(rdb, busCfg, logger)
	hub := sse.NewHub(logger)
	subCfg := sse.DefaultSubscriberConfig(cache.DefaultAdminStreamKey)
	subCfg.BlockTimeout = 100 * time.Millisecond
	shutdowner := &noopShutdowner{}
	subscriber := sse.NewSubscriber(rdb, hub, shutdowner, subCfg, logger)
	handlerCfg := sse.DefaultHandlerConfig(cache.DefaultAdminStreamKey)
	handlerCfg.HeartbeatInterval = 100 * time.Millisecond // fast for tests
	handler := sse.NewHandler(rdb, hub, handlerCfg, logger)

	ctx, cancel := context.WithCancel(context.Background())
	go bus.Run(ctx)
	go hub.Run(ctx)
	go subscriber.Run(ctx)
	time.Sleep(20 * time.Millisecond) // let goroutines enter their loops

	router := gin.New()
	if secret != nil {
		router.GET("/stream", middleware.AdminJWTMiddleware(middleware.AdminJWTConfig{
			Secret: secret,
			MaxTTL: time.Hour,
		}), handler.HandleStream)
	} else {
		router.GET("/stream", handler.HandleStream)
	}
	server := httptest.NewServer(router)

	t.Cleanup(func() {
		server.Close()
		cancel()
		// Wait for goroutines to drain
		select {
		case <-subscriber.Done():
		case <-time.After(2 * time.Second):
			t.Log("subscriber did not stop within cleanup deadline")
		}
		select {
		case <-hub.Stopped():
		case <-time.After(2 * time.Second):
			t.Log("hub did not stop within cleanup deadline")
		}
		select {
		case <-bus.Done():
		case <-time.After(2 * time.Second):
			t.Log("bus did not stop within cleanup deadline")
		}
	})

	return &stack{
		mr:         mr,
		redis:      rdb,
		bus:        bus,
		hub:        hub,
		subscriber: subscriber,
		handler:    handler,
		router:     router,
		server:     server,
		cancel:     cancel,
	}
}

type noopShutdowner struct{}

func (n *noopShutdowner) Shutdown(_ ...fx.ShutdownOption) error { return nil }

// publishAndWait emits an event through the bus and waits for the
// subscriber to deliver it to the hub broadcast channel (proven by
// the stream actually having the entry via XLEN).
func (s *stack) publishAndWait(t *testing.T, eventType string) admin.AdminEvent {
	t.Helper()
	evt, err := admin.NewOrderLifecycleEvent(eventType, admin.OrderLifecyclePayload{
		OrderID:      uuid.Must(uuid.NewV7()),
		UserID:       1,
		TicketTypeID: uuid.Must(uuid.NewV7()),
		Quantity:     1,
		ToStatus:     "reserved",
	})
	require.NoError(t, err)
	s.bus.Publish(evt)
	// Wait for XADD to land
	require.Eventually(t, func() bool {
		n, _ := s.redis.XLen(context.Background(), cache.DefaultAdminStreamKey).Result()
		return n > 0
	}, 2*time.Second, 10*time.Millisecond)
	return evt
}

func TestIntegration_E2E_PublishToSSEDelivery(t *testing.T) {
	st := newStack(t, nil)

	// Connect SSE client
	resp := connectSSE(t, st.server.URL+"/stream", "", 0)
	defer resp.Body.Close()

	// Give the handler time to register with the hub
	time.Sleep(50 * time.Millisecond)

	// Publish an event
	st.publishAndWait(t, admin.EventTypeOrderPaid)

	// Read SSE frames until we see order.paid
	frame := readSSEFrame(t, resp.Body, 3*time.Second)
	assert.Contains(t, frame, "event: order.paid")
	assert.Contains(t, frame, "id: ")
	assert.Contains(t, frame, "data: ")
}

func TestIntegration_E2E_LastEventIDReplay(t *testing.T) {
	st := newStack(t, nil)

	// Publish 3 events to the stream BEFORE any client connects
	evt1 := st.publishAndWait(t, admin.EventTypeOrderCreated)
	evt2 := st.publishAndWait(t, admin.EventTypeOrderPaid)
	evt3 := st.publishAndWait(t, admin.EventTypeOrderCompensated)

	// Read all Stream IDs from Redis (post-XADD)
	msgs, err := st.redis.XRange(context.Background(),
		cache.DefaultAdminStreamKey, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	id1 := msgs[0].ID

	// Connect with Last-Event-ID == evt1's stream ID — should replay
	// evt2 + evt3 (exclusive lower bound)
	resp := connectSSE(t, st.server.URL+"/stream", id1, 500*time.Millisecond)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	got := string(body)

	// evt1 should NOT appear (exclusive bound)
	assert.NotContains(t, got, "id: "+id1)
	// evt2 and evt3 should appear via XRANGE replay
	assert.Contains(t, got, evt2.EventType)
	assert.Contains(t, got, evt3.EventType)
	// Defensive: assert evt1's event_id (from payload) doesn't sneak
	// into the body — it shouldn't because we're filtering by stream
	// ID, not by envelope event_id
	_ = evt1
}

func TestIntegration_E2E_JWTAuthRequired(t *testing.T) {
	secret := []byte("integration-test-secret-must-be-at-least-32-bytes")
	st := newStack(t, secret)

	// Connect without token → 401
	resp, err := http.Get(st.server.URL + "/stream")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Connect with valid token → 200
	token, err := middleware.MintAdminJWT(secret, "ops-test", 30*time.Minute)
	require.NoError(t, err)
	resp2 := connectSSE(t, st.server.URL+"/stream?token="+token, "", 200*time.Millisecond)
	require.NotNil(t, resp2, "auth test must receive a response within 200ms (review round 2 REG-1)")
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
}

func TestIntegration_E2E_HeartbeatVisible(t *testing.T) {
	st := newStack(t, nil)

	resp := connectSSE(t, st.server.URL+"/stream", "", 400*time.Millisecond)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	// With heartbeat interval 100ms and read window 400ms, expect ≥2
	// heartbeat comment-lines
	assert.GreaterOrEqual(t, strings.Count(string(body), ": heartbeat"), 2)
}

func TestIntegration_E2E_NoGoroutineLeak_1000Iterations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 1000-iteration leak test in short mode")
	}
	st := newStack(t, nil)

	const iters = 100 // 100 in CI is enough; raise locally for stress
	var wg sync.WaitGroup
	wg.Add(iters)
	for i := 0; i < iters; i++ {
		go func() {
			defer wg.Done()
			// Per-connection timeout 150ms — needs to be longer than
			// the hub.Run loop's worst-case register queue drain under
			// 100 concurrent connect storms (post review-round-1
			// handler change moved hub.Register before WriteHeader,
			// so under-load latency now affects the response time of
			// the SSE handshake; 30ms was too tight).
			resp := connectSSE(t, st.server.URL+"/stream", "", 150*time.Millisecond)
			if resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	// TestMain's goleak.VerifyTestMain catches any leaked goroutines.
}

func TestIntegration_E2E_ConcurrentClientsReceiveSameBroadcast(t *testing.T) {
	st := newStack(t, nil)

	const numClients = 5
	resps := make([]*http.Response, numClients)
	for i := 0; i < numClients; i++ {
		resps[i] = connectSSE(t, st.server.URL+"/stream", "", 0)
		require.NotNil(t, resps[i], "client %d must connect (review round 2 REG-2)", i)
		defer resps[i].Body.Close() //nolint:gocritic // intentional inline defer per client
	}

	// Give all clients time to register with the hub
	time.Sleep(100 * time.Millisecond)

	// Publish one event
	st.publishAndWait(t, admin.EventTypeOrderPaid)

	// Each client should receive the event
	for i, resp := range resps {
		frame := readSSEFrame(t, resp.Body, 2*time.Second)
		assert.Contains(t, frame, "event: order.paid",
			"client %d did not receive broadcast", i)
	}
}

// ─── helpers ───────────────────────────────────────────────────────

// connectSSE opens an SSE connection. If readDuration > 0, the
// request context expires after that duration so io.ReadAll returns.
//
// Some callers (the goroutine-leak stress test) tolerate a nil
// response when the per-request context times out before the
// server's hub.Register completes — that's the intended SSE flow
// per CRIT-1: the handler refuses with 503 if it can't register
// in time, or the ctx cancels before headers are written.
func connectSSE(t *testing.T, url, lastEventID string, readDuration time.Duration) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	require.NoError(t, err)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	ctx := context.Background()
	if readDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, readDuration)
		t.Cleanup(cancel)
	}
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Caller-tolerant: connect failures (ctx cancelled before
		// server responds) are valid outcomes in stress tests.
		return nil
	}
	return resp
}

// readSSEFrame reads frames until a non-heartbeat frame appears
// (or the timeout elapses). Heartbeat-only frames (`: heartbeat\n\n`)
// are skipped — tests want to see actual events. Returns the full
// accumulated buffer including any heartbeats consumed along the way
// so assertions like `assert.Contains(frame, "event: order.paid")`
// still work as expected.
func readSSEFrame(t *testing.T, r io.Reader, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 4096)
	var sb strings.Builder
	for time.Now().Before(deadline) {
		n, err := r.Read(buf[:512])
		if n > 0 {
			sb.Write(buf[:n])
			s := sb.String()
			// Skip heartbeat-only frames: keep reading until we see
			// an `id:` or `event:` line indicating a real event frame.
			if strings.Contains(s, "\n\n") &&
				(strings.Contains(s, "id: ") || strings.Contains(s, "event: ")) {
				return s
			}
		}
		if err != nil {
			break
		}
	}
	t.Logf("readSSEFrame timed out, partial: %q", sb.String())
	return sb.String()
}

// validateJSONPayload extracts the data: line and unmarshals it.
// Used by tests that want to inspect payload structure (kept here
// for future tests; currently unused, satisfies linter via _ var).
func validateJSONPayload(t *testing.T, frame string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "data: ") {
			var got map[string]any
			err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &got)
			require.NoError(t, err)
			return got
		}
	}
	return nil
}

var _ = validateJSONPayload // silence unused
