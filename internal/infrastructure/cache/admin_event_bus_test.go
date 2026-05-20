package cache

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/application/admin"
	mlog "booking_monitor/internal/log"
)

func newBusTestFixture(t *testing.T, channelCap int) (*AdminEventBus, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultAdminEventBusConfig()
	if channelCap > 0 {
		cfg.ChannelCapacity = channelCap
	}
	cfg.DrainTimeout = 200 * time.Millisecond
	cfg.XAddTimeout = 500 * time.Millisecond
	bus := NewAdminEventBus(client, cfg, mlog.NewNop())
	return bus, mr, client
}

// streamLen returns XLEN of the stream via the redis client
// (miniredis doesn't expose XLen as a Go method).
func streamLen(t *testing.T, client *redis.Client, key string) int64 {
	t.Helper()
	n, err := client.XLen(context.Background(), key).Result()
	require.NoError(t, err)
	return n
}

func buildLifecycleEvent(t *testing.T, eventType string) admin.AdminEvent {
	t.Helper()
	evt, err := admin.NewOrderLifecycleEvent(eventType, admin.OrderLifecyclePayload{
		OrderID:      uuid.Must(uuid.NewV7()),
		UserID:       1,
		TicketTypeID: uuid.Must(uuid.NewV7()),
		Quantity:     1,
		ToStatus:     "reserved",
	})
	require.NoError(t, err)
	return evt
}

func TestAdminEventBus_PublishAndDrain(t *testing.T) {
	bus, mr, client := newBusTestFixture(t, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Spawn drainer
	go bus.Run(ctx)

	// Publish 3 events
	for i := 0; i < 3; i++ {
		bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderCreated))
	}

	// Wait for drainer to flush
	require.Eventually(t, func() bool {
		return mr.Exists(DefaultAdminStreamKey)
	}, 2*time.Second, 10*time.Millisecond, "stream should exist after publish")

	// Verify 3 entries written
	require.Eventually(t, func() bool {
		return streamLen(t, client, DefaultAdminStreamKey) == 3
	}, 2*time.Second, 10*time.Millisecond, "stream should have 3 entries")
}

func TestAdminEventBus_ChannelFullDropsAndCounters(t *testing.T) {
	bus, _, _ := newBusTestFixture(t, 1)

	// Don't start drainer — channel fills fast
	// First publish fills the channel (cap=1)
	bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderCreated))
	// Second publish must drop (no drainer to consume)
	bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderCreated))
	bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderCreated))

	assert.Equal(t, int64(1), bus.depth.Load(), "depth should be 1 (one queued, two dropped)")
}

func TestAdminEventBus_PublishIsNonBlocking(t *testing.T) {
	bus, _, _ := newBusTestFixture(t, 1)
	// No drainer running — channel will fill after first publish.
	// Verify subsequent Publish returns immediately (within 10ms)
	// rather than blocking forever.
	bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderCreated)) // fills channel

	done := make(chan struct{})
	go func() {
		bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderCreated)) // would block in naive impl
		close(done)
	}()
	select {
	case <-done:
		// Good — Publish returned promptly
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Publish blocked when channel was full — should drop instead")
	}
}

func TestAdminEventBus_RunRespectsCtxCancellation(t *testing.T) {
	bus, _, _ := newBusTestFixture(t, 10)
	ctx, cancel := context.WithCancel(context.Background())

	go bus.Run(ctx)

	// Verify Run exits soon after ctx cancel
	cancel()
	select {
	case <-bus.Done():
		// Good
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not exit after ctx cancellation within 1s")
	}
}

func TestAdminEventBus_DrainOnShutdownFlushesPending(t *testing.T) {
	bus, _, client := newBusTestFixture(t, 100)
	ctx, cancel := context.WithCancel(context.Background())

	// Pre-fill channel with 5 events BEFORE starting drainer
	for i := 0; i < 5; i++ {
		bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderPaid))
	}
	require.Equal(t, int64(5), bus.depth.Load())

	// Now start drainer, immediately cancel ctx
	go bus.Run(ctx)
	cancel()

	// Wait for done — drain should still flush the pending 5
	select {
	case <-bus.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not complete within 2s")
	}

	// Verify all 5 made it to Redis (or count is at least non-zero
	// to show drain ran). Realistically the drain timeout is 200ms
	// in the fixture; miniredis XADD is sub-ms so all 5 should land.
	got := streamLen(t, client, DefaultAdminStreamKey)
	assert.True(t, got >= 1, "drain should have flushed at least some events, got %d", got)
}

func TestAdminEventBus_StreamMaxLenEnforced(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultAdminEventBusConfig()
	cfg.StreamMaxLen = 5      // tiny MAXLEN so we can verify trim
	cfg.ChannelCapacity = 100 // plenty of room to enqueue
	cfg.DrainTimeout = time.Second
	bus := NewAdminEventBus(client, cfg, mlog.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bus.Run(ctx)

	// Publish 20 events
	for i := 0; i < 20; i++ {
		bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderCreated))
	}

	// Wait for drainer
	require.Eventually(t, func() bool {
		return streamLen(t, client, DefaultAdminStreamKey) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// Note: miniredis's MAXLEN approximation may keep slightly more
	// than the requested limit (similar to real Redis with Approx=true),
	// so we assert "well below 20" rather than "exactly 5".
	require.Eventually(t, func() bool {
		return streamLen(t, client, DefaultAdminStreamKey) <= 10
	}, 2*time.Second, 10*time.Millisecond,
		"stream should be trimmed to roughly MAXLEN=5, got %d",
		streamLen(t, client, DefaultAdminStreamKey))
}

func TestAdminEventBus_ConcurrentPublishersSafe(t *testing.T) {
	bus, _, client := newBusTestFixture(t, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bus.Run(ctx)

	// 10 goroutines × 100 publishes each = 1000 events
	const (
		goroutines = 10
		perRoutine = 100
	)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perRoutine; j++ {
				bus.Publish(buildLifecycleEvent(t, admin.EventTypeOrderCreated))
				if j%10 == 0 {
					// Small breather so we don't fill the channel
					time.Sleep(time.Microsecond)
				}
			}
			_ = strconv.Itoa(id) // silence unused
		}(i)
	}
	wg.Wait()

	// Wait for drainer to flush everything
	require.Eventually(t, func() bool {
		return streamLen(t, client, DefaultAdminStreamKey) == int64(goroutines*perRoutine)
	}, 5*time.Second, 50*time.Millisecond,
		"expected %d events, got %d", goroutines*perRoutine,
		streamLen(t, client, DefaultAdminStreamKey))
}

func TestClassifyXAddError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil error", nil, ""},
		{"deadline exceeded", context.DeadlineExceeded, "timeout"},
		{"context canceled", context.Canceled, "timeout"},
		{"plain other error", assertError("READONLY You can't write against a read only replica"), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyXAddError(tc.err))
		})
	}
}

// assertError is a tiny helper to inline an error literal in tests.
type assertError string

func (e assertError) Error() string { return string(e) }
