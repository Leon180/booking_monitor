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

// waitForActiveClients blocks until hub.ActiveClients() observes the
// expected count or the timeout expires. Replaces the earlier
// time.Sleep(20ms) "drainRegister" helper, which raced on slow CI
// runners: with both `register` and `broadcast` channels buffered,
// Go's select picks randomly when multiple cases are ready, so the
// broadcast could be processed before the register lands in the
// clients map — causing the slow-consumer drop test to silently
// miss its target. Polling the atomic counter eliminates that race.
func waitForActiveClients(t *testing.T, hub *Hub, expected int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		return hub.ActiveClients() == expected
	}, 2*time.Second, 5*time.Millisecond,
		"hub never reached %d active clients (current=%d)", expected, hub.ActiveClients())
}

func TestHub_RegisterReceivesBroadcast(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	c := NewClient()
	require.True(t, hub.Register(context.Background(), c))
	waitForActiveClients(t, hub, 1)

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
}

func TestHub_UnregisterClosesSendChan(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	c := NewClient()
	hub.Register(context.Background(), c)
	waitForActiveClients(t, hub, 1)
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
	waitForActiveClients(t, hub, 1)

	// Fill the client's Send chan (cap = 100)
	for i := 0; i < ClientSendBufferCapacity; i++ {
		c.Send <- redis.XMessage{ID: "x-0"}
	}

	// Now broadcast — should drop the client. Crucially we do NOT
	// drain c.Send before the hub has had a chance to attempt the
	// non-blocking send; otherwise the drain races the hub goroutine
	// and the cap-full → drop branch never fires (the previous
	// version of this test polled c.Send in a tight loop right
	// after Broadcast, which on slow CI workers raced the test ahead
	// of the hub and made the channel empty by the time the hub
	// tried its `case c.Send <- msg:`, turning the drop path into
	// a successful send and a never-closed channel).
	hub.Broadcast(context.Background(), redis.XMessage{
		ID:     "1-0",
		Values: map[string]any{"event_type": "test"},
	})

	// Sync on the drop happening: hub.Run decrements activeClients
	// inside the slow-consumer branch (right next to close(c.Send)).
	// Polling that atomic counter is race-free — when it reaches 0
	// we know the close has already executed.
	waitForActiveClients(t, hub, 0)

	// Now safe to drain + verify closed.
	drained := 0
	for range ClientSendBufferCapacity {
		select {
		case _, open := <-c.Send:
			if !open {
				return // closed mid-drain — also acceptable
			}
			drained++
		default:
			// channel empty before draining all buffered messages —
			// shouldn't happen because the hub closes after the
			// send branch fails (i.e., buffered messages remain).
		}
	}
	// Buffered messages drained — channel must be closed now.
	_, open := <-c.Send
	require.False(t, open, "slow client's Send chan should be closed (drained %d msgs)", drained)
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
	waitForActiveClients(t, hub, numClients)

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
