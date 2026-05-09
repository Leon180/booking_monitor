package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// PR-D12.4 Slice 4 saga-consumer-side tests:
//
//   - shouldResetLag: pure-function unit tests with synthetic
//     time advancement (no real-time sleeps, no clock injection).
//   - runIdleReset lifecycle: companion goroutine returns on
//     ctx.Done within a bounded window, no goroutine leak.
//
// Real-time-driven tests of the 5s ticker + 30s threshold would
// be slow (35s worst case) and flaky on CI. The shouldResetLag
// extraction keeps the testable surface pure; the lifecycle test
// uses a real ticker but with a tight ctx-cancel window.

// TestShouldResetLag_TableDriven pins the idle-reset decision
// function across the matrix of (lastUnixNano, now, threshold).
// Covers the three operationally-meaningful cases:
//
//   - lastUnixNano == 0 sentinel: consumer hasn't seen any
//     message since boot → reset (so gauge starts at 0)
//   - last < threshold ago: consumer is busy → DON'T reset
//   - last > threshold ago: consumer is idle → reset
//
// A regression that flipped the sentinel sign or the comparison
// direction lands here as a failing assertion.
func TestShouldResetLag_TableDriven(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	// Reference the production constant directly so a future tweak to the
	// threshold (e.g. 30s → 60s) doesn't silently leave this test asserting
	// the old boundary. The boundary case names below ("29s_ago", "31s_ago")
	// are intentionally written against the current 30s value — if the
	// production constant moves, this test should fail to compile or the
	// boundary names should be updated in lockstep.
	threshold := idleResetThreshold

	cases := []struct {
		name         string
		lastUnixNano int64
		want         bool
		why          string
	}{
		{
			name:         "zero_sentinel_means_reset",
			lastUnixNano: 0,
			want:         true,
			why:          "consumer hasn't seen any message since boot — gauge should read 0 from boot until first message",
		},
		{
			name:         "just_now_means_dont_reset",
			lastUnixNano: now.UnixNano(),
			want:         false,
			why:          "message processed at exactly now; lag is fresh, gauge value still meaningful",
		},
		{
			name:         "1s_ago_means_dont_reset",
			lastUnixNano: now.Add(-1 * time.Second).UnixNano(),
			want:         false,
			why:          "1s of idle is well under the 30s threshold",
		},
		{
			name:         "29s_ago_means_dont_reset",
			lastUnixNano: now.Add(-29 * time.Second).UnixNano(),
			want:         false,
			why:          "boundary minus-1s — still under threshold",
		},
		{
			name:         "exactly_30s_ago_means_dont_reset",
			lastUnixNano: now.Add(-30 * time.Second).UnixNano(),
			want:         false,
			why:          "boundary equality: > threshold (NOT >=) so 30s exactly does not trigger",
		},
		{
			name:         "31s_ago_means_reset",
			lastUnixNano: now.Add(-31 * time.Second).UnixNano(),
			want:         true,
			why:          "boundary plus-1s — crossed threshold, idle long enough",
		},
		{
			name:         "1h_ago_means_reset",
			lastUnixNano: now.Add(-1 * time.Hour).UnixNano(),
			want:         true,
			why:          "long idle — gauge value is stale, reset to 0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldResetLag(tc.lastUnixNano, now, threshold)
			assert.Equal(t, tc.want, got, "%s — %s", tc.name, tc.why)
		})
	}
}

// TestRunIdleReset_ExitsOnCtxDone pins that the companion
// goroutine spawned by `Start` exits cleanly when ctx is
// cancelled. A regression that forgot the ctx.Done case (or used
// a non-cancellable context) would leak goroutines per Start
// invocation; under the project's `go test -race` discipline this
// would surface eventually as the goroutine count climbed.
//
// Bounded by a 200ms timeout so a regression presents as a clean
// test failure, not a hung suite.
func TestRunIdleReset_ExitsOnCtxDone(t *testing.T) {
	t.Parallel()

	c := &SagaConsumer{metrics: nopMetricsRecorder{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		c.runIdleReset(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Goroutine returned within the cancellation budget.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runIdleReset did not exit within 200ms of ctx cancellation — goroutine leak risk")
	}
}

// nopMetricsRecorder satisfies saga.CompensatorMetrics with
// zero side effects. Used by lifecycle tests that don't assert
// on metrics calls.
//
// TODO(d12-test-debt): integration test driving the 5s ticker +
// 30s threshold against real time. Blocked on idleResetThreshold
// + idleResetTickInterval being package-level `const` (not `var`)
// — overriding them via linker tricks isn't worth the complexity.
// The pure-function `shouldResetLag` is exhaustively covered above
// (covers the decision); `runIdleReset` is a 3-line orchestration
// (`shouldResetLag(c.lastMessageAt.Load(), time.Now(),
// idleResetThreshold)` per tick) so visual inspection is
// sufficient once the decision function is pinned. Path forward
// when a future contributor wants this: promote the two constants
// to `var` and restore them via `t.Cleanup` in the test. Deferred
// because no current test needs it.
type nopMetricsRecorder struct{}

func (nopMetricsRecorder) RecordEventProcessed(string)       {}
func (nopMetricsRecorder) ObserveLoopDuration(time.Duration) {}
func (nopMetricsRecorder) SetConsumerLag(time.Duration)      {}
