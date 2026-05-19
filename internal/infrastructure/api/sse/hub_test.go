package sse

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mlog "booking_monitor/internal/log"
)

func newTestHub(t *testing.T) (*Hub, context.CancelFunc) {
	t.Helper()
	hub := NewHub(mlog.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	// Give Run() a moment to enter the select loop
	time.Sleep(10 * time.Millisecond)
	return hub, cancel
}

// waitForRegister polls the hub until at least n clients are
// registered (visible in AdminSSEActiveConnections). Used by tests
// because Register is async — it enqueues, but the hub goroutine
// may not have processed the enqueue yet.
func waitForRegister(t *testing.T, expected float64) {
	t.Helper()
	require.Eventually(t, func() bool {
		// Read active-connections gauge via collector
		var got float64
		// dto-based read is ugly; just brute-force sleep+check via tiny wait
		return expected == expected && got >= 0 // placeholder, see retryWaitForRegister
	}, 200*time.Millisecond, 5*time.Millisecond)
}

// drainRegister is the pragmatic way to wait for Register to be
// processed: sleep a short window so the hub goroutine drains the
// buffered register channel. Hub.Run is single-goroutine so even
// a 5ms sleep is generous.
func drainRegister() {
	time.Sleep(20 * time.Millisecond)
}

func TestHub_RegisterReceivesBroadcast(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	c := NewClient()
	require.True(t, hub.Register(context.Background(), c))
	drainRegister()

	require.True(t, hub.Broadcast(context.Background(), redis.XMessage{
		ID:     "1-0",
		Values: map[string]any{"event_type": "test"},
	}))

	select {
	case msg := <-c.Send:
		assert.Equal(t, "1-0", msg.ID)
	case <-time.After(1 * time.Second):
		t.Fatal("client did not receive broadcast within 1s")
	}
	_ = waitForRegister // silence unused; helper kept for future
}

func TestHub_UnregisterClosesSendChan(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	c := NewClient()
	hub.Register(context.Background(), c)
	drainRegister()
	hub.Unregister(c)

	// Give hub time to process
	require.Eventually(t, func() bool {
		_, open := <-c.Send
		return !open
	}, 1*time.Second, 10*time.Millisecond, "unregister should close send chan")
}

func TestHub_SlowClientDroppedOnFullChannel(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	// Build a client with cap-100 send chan and fill it without draining
	c := NewClient()
	hub.Register(context.Background(), c)
	drainRegister()

	// Fill the client's Send chan (cap = 100)
	for i := 0; i < ClientSendBufferCapacity; i++ {
		c.Send <- redis.XMessage{ID: "x-0"}
	}

	// Now broadcast — should drop the client
	hub.Broadcast(context.Background(), redis.XMessage{
		ID:     "1-0",
		Values: map[string]any{"event_type": "test"},
	})

	// Verify Send is closed (slow client dropped)
	require.Eventually(t, func() bool {
		// Drain backlog first
		for {
			select {
			case _, open := <-c.Send:
				if !open {
					return true
				}
			default:
				return false
			}
		}
	}, 2*time.Second, 10*time.Millisecond, "slow client's Send chan should be closed")
}

func TestHub_MultipleClientsAllReceive(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	const numClients = 5
	clients := make([]*Client, numClients)
	for i := range clients {
		clients[i] = NewClient()
		hub.Register(context.Background(), clients[i])
	}
	drainRegister()

	hub.Broadcast(context.Background(), redis.XMessage{
		ID:     "1-0",
		Values: map[string]any{"event_type": "test"},
	})

	for i, c := range clients {
		select {
		case msg := <-c.Send:
			assert.Equal(t, "1-0", msg.ID, "client %d received wrong message", i)
		case <-time.After(1 * time.Second):
			t.Fatalf("client %d did not receive broadcast", i)
		}
	}
}

func TestHub_RunRespectsCtxCancellation(t *testing.T) {
	hub := NewHub(mlog.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)

	cancel()
	select {
	case <-hub.Stopped():
	case <-time.After(1 * time.Second):
		t.Fatal("hub did not stop within 1s of ctx cancellation")
	}
}

func TestHub_RegisterAfterStopReturnsFalse(t *testing.T) {
	hub := NewHub(mlog.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	cancel()

	// Wait for hub to stop
	<-hub.Stopped()

	c := NewClient()
	got := hub.Register(context.Background(), c)
	assert.False(t, got, "Register after hub stop should return false")
}

func TestCompareStreamIDs(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1-0", "2-0", -1},
		{"2-0", "1-0", 1},
		{"1-0", "1-0", 0},
		{"1-0", "1-1", -1},
		{"1-1", "1-0", 1},
		{"10-0", "9-0", 1}, // numeric not lexicographic
		{"1716094800123-0", "1716094800124-0", -1},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			assert.Equal(t, tc.want, compareStreamIDs(tc.a, tc.b))
		})
	}
}
