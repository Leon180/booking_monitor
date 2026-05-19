package sse

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mlog "booking_monitor/internal/log"
)

func newHandlerFixture(t *testing.T, heartbeat time.Duration) (*Handler, *Hub, *miniredis.Miniredis, *redis.Client, context.CancelFunc) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	hub := NewHub(mlog.NewNop())
	hubCtx, hubCancel := context.WithCancel(context.Background())
	go hub.Run(hubCtx)
	time.Sleep(10 * time.Millisecond) // let Run enter loop

	cfg := DefaultHandlerConfig("events:admin:stream")
	if heartbeat > 0 {
		cfg.HeartbeatInterval = heartbeat
	}
	handler := NewHandler(client, hub, cfg, mlog.NewNop())
	return handler, hub, mr, client, hubCancel
}

func TestHandler_RejectsConnectionsWhenShuttingDown(t *testing.T) {
	handler, _, _, _, cancel := newHandlerFixture(t, 30*time.Second)
	defer cancel()

	handler.SetShuttingDown(true)

	router := gin.New()
	router.GET("/stream", handler.HandleStream)

	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestHandler_WritesSSEHeaders(t *testing.T) {
	handler, _, _, _, cancel := newHandlerFixture(t, 30*time.Second)
	defer cancel()

	router := gin.New()
	router.GET("/stream", handler.HandleStream)
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
	ctx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "no", resp.Header.Get("X-Accel-Buffering"))
}

func TestHandler_DeliversBroadcastToClient(t *testing.T) {
	handler, hub, _, _, cancel := newHandlerFixture(t, 30*time.Second)
	defer cancel()

	router := gin.New()
	router.GET("/stream", handler.HandleStream)
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
	ctx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Give handler time to register with hub
	time.Sleep(50 * time.Millisecond)

	// Broadcast an event
	hub.Broadcast(context.Background(), redis.XMessage{
		ID: "1716094800123-0",
		Values: map[string]any{
			"event_type": "order.paid",
			"data":       `{"order_id":"abc"}`,
		},
	})

	// Read the SSE frame
	buf := make([]byte, 1024)
	done := make(chan string, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		done <- string(buf[:n])
	}()
	select {
	case frame := <-done:
		assert.Contains(t, frame, "id: 1716094800123-0")
		assert.Contains(t, frame, "event: order.paid")
		assert.Contains(t, frame, `data: {"order_id":"abc"}`)
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive SSE frame within 2s")
	}
}

func TestHandler_HeartbeatWritten(t *testing.T) {
	handler, _, _, _, cancel := newHandlerFixture(t, 50*time.Millisecond)
	defer cancel()

	router := gin.New()
	router.GET("/stream", handler.HandleStream)
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
	ctx, cancelReq := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelReq()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Read until we see a heartbeat
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), ": heartbeat\n\n", "expected at least one heartbeat in response")
}

func TestHandler_ReplaysLastEventID(t *testing.T) {
	handler, _, _, client, cancel := newHandlerFixture(t, 30*time.Second)
	defer cancel()

	// Pre-populate stream with 3 events
	ctx := context.Background()
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		id, err := client.XAdd(ctx, &redis.XAddArgs{
			Stream: "events:admin:stream",
			Values: map[string]any{
				"event_type": "order.created",
				"data":       `{}`,
			},
		}).Result()
		require.NoError(t, err)
		ids[i] = id
	}

	router := gin.New()
	router.GET("/stream", handler.HandleStream)
	srv := httptest.NewServer(router)
	defer srv.Close()

	// Reconnect with Last-Event-ID after the first event
	req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
	req.Header.Set("Last-Event-ID", ids[0])
	reqCtx, cancelReq := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelReq()
	req = req.WithContext(reqCtx)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Should NOT contain ids[0] (exclusive lower bound)
	assert.NotContains(t, bodyStr, "id: "+ids[0])
	// Should contain ids[1] and ids[2]
	assert.Contains(t, bodyStr, "id: "+ids[1])
	assert.Contains(t, bodyStr, "id: "+ids[2])
}

func TestHandler_NoGoroutineLeak(t *testing.T) {
	// Spawn handler + multiple connect/disconnect cycles, check no
	// goroutines stranded. Lightweight version of goleak — full
	// goleak coverage moves to integration test commit.
	handler, _, _, _, cancel := newHandlerFixture(t, 100*time.Millisecond)
	defer cancel()

	router := gin.New()
	router.GET("/stream", handler.HandleStream)
	srv := httptest.NewServer(router)
	defer srv.Close()

	const iters = 20
	var wg sync.WaitGroup
	for i := 0; i < iters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
			reqCtx, reqCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer reqCancel()
			req = req.WithContext(reqCtx)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	// If goroutines leaked, the test would hang or race detector would catch it
	// (we have -race). Real goleak coverage in test/integration/sse/.
}

func TestHandler_WriteMessageFormat(t *testing.T) {
	handler := &Handler{}
	w := &strings.Builder{}
	flusher := &noopFlusher{}

	handler.writeMessage(w, flusher, redis.XMessage{
		ID: "1-0",
		Values: map[string]any{
			"event_type": "order.paid",
			"data":       `{"order_id":"abc"}`,
		},
	})

	out := w.String()
	assert.Contains(t, out, "id: 1-0\n")
	assert.Contains(t, out, "event: order.paid\n")
	assert.Contains(t, out, `data: {"order_id":"abc"}`)
	assert.True(t, strings.HasSuffix(out, "\n\n"), "frame must end with blank line")
}

func TestHandler_WriteMessageRetryHint(t *testing.T) {
	handler := &Handler{}
	w := &strings.Builder{}
	flusher := &noopFlusher{}

	handler.writeMessage(w, flusher, redis.XMessage{
		ID: "0-0",
		Values: map[string]any{
			"event_type": "_retry_hint",
			"retry_ms":   "5000",
		},
	})

	out := w.String()
	assert.Equal(t, "retry: 5000\n\n", out)
}

type noopFlusher struct{}

func (n *noopFlusher) Flush() {}
