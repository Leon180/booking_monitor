package outbox_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"booking_monitor/internal/application/outbox"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/mocks"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRelay_ProcessBatch(t *testing.T) {
	ctx := mlog.NewContext(context.Background(), mlog.NewNop(), "")

	id1 := uuid.New()
	id2 := uuid.New()

	tests := []struct {
		name       string
		batchSize  int
		setupMocks func(*mocks.MockOutboxRepository, *mocks.MockEventPublisher)
	}{
		{
			name:      "Success - Publish and Mark Processed",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				events := []domain.OutboxEvent{
					domain.ReconstructOutboxEvent(id1, "test.event", []byte("payload1"), domain.OutboxStatusPending, time.Now().UTC(), nil),
					domain.ReconstructOutboxEvent(id2, "test.event", []byte("payload2"), domain.OutboxStatusPending, time.Now().UTC(), nil),
				}

				// 1. ListPending
				repo.EXPECT().ListPending(ctx, 10).Return(events, nil)

				// 2. Publish & Mark Processed (Event 1).
				//    gomock.Eq pins the exact event published — without
				//    it a regression that published the WRONG entity
				//    would still pass the test (per slice-0 go-reviewer
				//    M2). domain.OutboxEvent is a value type with all
				//    unexported fields; reflect.DeepEqual via gomock.Eq
				//    works because both events were constructed by the
				//    same path.
				pub.EXPECT().PublishOutboxEvent(ctx, gomock.Eq(events[0])).Return(nil)
				repo.EXPECT().MarkProcessed(ctx, id1).Return(nil)

				// 3. Publish & Mark Processed (Event 2)
				pub.EXPECT().PublishOutboxEvent(ctx, gomock.Eq(events[1])).Return(nil)
				repo.EXPECT().MarkProcessed(ctx, id2).Return(nil)
			},
		},
		{
			name:      "Repo Error - ListPending Fails",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				repo.EXPECT().ListPending(ctx, 10).Return(nil, errors.New("db error"))
			},
		},
		{
			name:      "Publisher Error - Skip MarkProcessed",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				events := []domain.OutboxEvent{
					domain.ReconstructOutboxEvent(id1, "test.event", []byte("payload1"), domain.OutboxStatusPending, time.Now().UTC(), nil),
				}

				repo.EXPECT().ListPending(ctx, 10).Return(events, nil)

				// Publish Fails
				pub.EXPECT().PublishOutboxEvent(ctx, gomock.Any()).Return(errors.New("kafka error"))

				// MarkProcessed Should NOT be called
			},
		},
		{
			name:      "Repo Error - MarkProcessed Fails",
			batchSize: 10,
			setupMocks: func(repo *mocks.MockOutboxRepository, pub *mocks.MockEventPublisher) {
				events := []domain.OutboxEvent{
					domain.ReconstructOutboxEvent(id1, "test.event", []byte("payload1"), domain.OutboxStatusPending, time.Now().UTC(), nil),
				}

				repo.EXPECT().ListPending(ctx, 10).Return(events, nil)

				// Publish Success
				pub.EXPECT().PublishOutboxEvent(ctx, gomock.Any()).Return(nil)

				// MarkProcessed Fails (Log and Continue)
				repo.EXPECT().MarkProcessed(ctx, id1).Return(errors.New("db update error"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRepo := mocks.NewMockOutboxRepository(ctrl)
			mockPub := mocks.NewMockEventPublisher(ctrl)
			mockLock := mocks.NewMockDistributedLock(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockRepo, mockPub)
			}

			// We test the private method processBatch directly to avoid timing issues with Run()
			relay := outbox.NewRelay(mockRepo, mockPub, tt.batchSize, mockLock, mlog.NewNop())
			relay.ProcessBatchForTest(ctx)
		})
	}
}

// Run-loop coverage. The Run loop has four branches that processBatch
// tests don't reach:
//   1. ctx cancelled before any tick   → return immediately, never-leader
//      path means Unlock MUST NOT be called.
//   2. TryLock returns error            → log + continue without panicking,
//      never-leader path preserved.
//   3. TryLock returns false (standby)  → batchFn never invoked, never-
//      leader path preserved.
//   4. TryLock returns true             → batchFn invoked exactly once
//      per tick; on ctx cancel afterwards, Unlock MUST be called via
//      the defer (closes the lock-leak bug fixed in PR #54 era).
//
// All four must run under -race because the Run loop spawns a goroutine.
// Tests use the RunWithBatchHookForTest seam to inject a deterministic
// batchFn that signals via channel rather than waiting on a real ticker
// for branches that don't need a tick.

const runLoopWaitForTick = 700 * time.Millisecond // poll interval is 500ms; 700ms gives one tick + slack

// TestRun_CtxCancelledBeforeTick: ctx is cancelled before the goroutine
// can complete a tick. Loop returns via the ctx.Done case; wasLeader
// stays false; Unlock MUST NOT be called.
func TestRun_CtxCancelledBeforeTick(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	mockRepo := mocks.NewMockOutboxRepository(ctrl)
	mockPub := mocks.NewMockEventPublisher(ctrl)
	mockLock := mocks.NewMockDistributedLock(ctrl)
	// No EXPECT() calls — neither TryLock nor Unlock should fire.

	relay := outbox.NewRelay(mockRepo, mockPub, 10, mockLock, mlog.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE Run starts the loop

	done := make(chan struct{})
	go func() {
		relay.RunWithBatchHookForTest(ctx, func(_ context.Context) error {
			t.Errorf("batchFn must not be called when ctx is cancelled before first tick")
			return nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(runLoopWaitForTick):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

// TestRun_TryLockError_ContinuesWithoutPanic: TryLock returns an error
// → loop logs + continues without panicking. wasLeader stays false →
// Unlock MUST NOT be called on exit.
//
// MinTimes(2) is load-bearing: a regression that swapped `continue`
// for `return` (or `break`) inside the error branch would still call
// TryLock once, then exit. Requiring ≥2 calls catches that — the test
// becomes a real verification of "loops" not just "doesn't panic."
//
// Driven via a signal channel rather than time.Sleep so the test is
// deterministic on slow CI runners.
func TestRun_TryLockError_ContinuesWithoutPanic(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	mockRepo := mocks.NewMockOutboxRepository(ctrl)
	mockPub := mocks.NewMockEventPublisher(ctrl)
	mockLock := mocks.NewMockDistributedLock(ctrl)

	var lockCount atomic.Int32
	twoCallsSignal := make(chan struct{})
	mockLock.EXPECT().TryLock(gomock.Any(), gomock.Eq(outbox.OutboxLockIDForTest)).
		DoAndReturn(func(_ context.Context, _ int64) (bool, error) {
			if lockCount.Add(1) == 2 {
				close(twoCallsSignal)
			}
			return false, errors.New("postgres: connection refused")
		}).MinTimes(2)
	// Unlock NOT expected.

	relay := outbox.NewRelay(mockRepo, mockPub, 10, mockLock, mlog.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		relay.RunWithBatchHookForTest(ctx, func(_ context.Context) error {
			t.Errorf("batchFn must not be called when TryLock errors")
			return nil
		})
		close(done)
	}()

	select {
	case <-twoCallsSignal:
	case <-time.After(2 * runLoopWaitForTick):
		cancel()
		t.Fatalf("loop did not iterate twice (got %d TryLock calls) — regression suspected: continue → return", lockCount.Load())
	}
	cancel()

	select {
	case <-done:
	case <-time.After(runLoopWaitForTick):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

// TestRun_StandbyReplica_BatchFnNotCalled: TryLock returns false (this
// replica lost leader election). batchFn MUST NOT be called; wasLeader
// stays false; Unlock MUST NOT be called on exit.
//
// MinTimes(2) + signal channel for the same reason as the
// TryLockError test — verifies the loop continues, not just that
// it didn't panic.
func TestRun_StandbyReplica_BatchFnNotCalled(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	mockRepo := mocks.NewMockOutboxRepository(ctrl)
	mockPub := mocks.NewMockEventPublisher(ctrl)
	mockLock := mocks.NewMockDistributedLock(ctrl)

	var lockCount atomic.Int32
	twoCallsSignal := make(chan struct{})
	mockLock.EXPECT().TryLock(gomock.Any(), gomock.Eq(outbox.OutboxLockIDForTest)).
		DoAndReturn(func(_ context.Context, _ int64) (bool, error) {
			if lockCount.Add(1) == 2 {
				close(twoCallsSignal)
			}
			return false, nil
		}).MinTimes(2)
	// Unlock NOT expected — never the leader.

	relay := outbox.NewRelay(mockRepo, mockPub, 10, mockLock, mlog.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		relay.RunWithBatchHookForTest(ctx, func(_ context.Context) error {
			t.Errorf("batchFn must not run on a standby replica")
			return nil
		})
		close(done)
	}()

	select {
	case <-twoCallsSignal:
	case <-time.After(2 * runLoopWaitForTick):
		cancel()
		t.Fatalf("loop did not iterate twice (got %d TryLock calls) — regression suspected: continue → return", lockCount.Load())
	}
	cancel()

	select {
	case <-done:
	case <-time.After(runLoopWaitForTick):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

// TestRun_LeaderRunsBatchAndUnlocksOnExit: TryLock succeeds (this
// replica is the leader). batchFn runs at least once. On ctx cancel,
// the deferred Unlock MUST fire via the wasLeader=true path. This
// closes the lock-leak bug from the relay's pre-PR-#54 era where
// Unlock was only called inside the ctx.Done branch.
func TestRun_LeaderRunsBatchAndUnlocksOnExit(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	mockRepo := mocks.NewMockOutboxRepository(ctrl)
	mockPub := mocks.NewMockEventPublisher(ctrl)
	mockLock := mocks.NewMockDistributedLock(ctrl)

	mockLock.EXPECT().TryLock(gomock.Any(), gomock.Eq(outbox.OutboxLockIDForTest)).
		Return(true, nil).MinTimes(1)
	// Unlock MUST be called exactly once (via the defer on Run exit).
	unlockSignal := make(chan struct{})
	mockLock.EXPECT().Unlock(gomock.Any(), gomock.Eq(outbox.OutboxLockIDForTest)).
		DoAndReturn(func(_ context.Context, _ int64) error {
			close(unlockSignal)
			return nil
		}).Times(1)

	relay := outbox.NewRelay(mockRepo, mockPub, 10, mockLock, mlog.NewNop())

	var batchCount atomic.Int32
	batchSignal := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		relay.RunWithBatchHookForTest(ctx, func(_ context.Context) error {
			batchCount.Add(1)
			select {
			case batchSignal <- struct{}{}:
			default:
			}
			return nil
		})
		close(done)
	}()

	// Wait for batchFn to fire at least once, then cancel.
	select {
	case <-batchSignal:
	case <-time.After(2 * runLoopWaitForTick):
		cancel()
		t.Fatal("batchFn did not run within 2 ticks")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(runLoopWaitForTick):
		t.Fatal("Run did not return after ctx cancellation")
	}

	// Verify Unlock fired.
	select {
	case <-unlockSignal:
	default:
		t.Fatal("Unlock was not called via the defer on Run exit")
	}

	require.GreaterOrEqual(t, batchCount.Load(), int32(1))
}

// TestRun_LeaderBatchFnError_LoopContinues: when batchFn returns an
// error, the loop logs it and keeps polling. The ticker MUST NOT
// stop. Verify by counting that batchFn is called multiple times even
// though every call returns an error.
func TestRun_LeaderBatchFnError_LoopContinues(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	mockRepo := mocks.NewMockOutboxRepository(ctrl)
	mockPub := mocks.NewMockEventPublisher(ctrl)
	mockLock := mocks.NewMockDistributedLock(ctrl)

	mockLock.EXPECT().TryLock(gomock.Any(), gomock.Eq(outbox.OutboxLockIDForTest)).
		Return(true, nil).MinTimes(1)
	mockLock.EXPECT().Unlock(gomock.Any(), gomock.Eq(outbox.OutboxLockIDForTest)).
		Return(nil).Times(1)

	relay := outbox.NewRelay(mockRepo, mockPub, 10, mockLock, mlog.NewNop())

	var batchCount atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		relay.RunWithBatchHookForTest(ctx, func(_ context.Context) error {
			batchCount.Add(1)
			return errors.New("simulated batch error")
		})
		close(done)
	}()

	// Wait long enough for at least 2 ticks (poll interval is 500ms).
	time.Sleep(1500 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(runLoopWaitForTick):
		t.Fatal("Run did not return after ctx cancellation")
	}

	assert.GreaterOrEqual(t, batchCount.Load(), int32(2),
		"batchFn must be re-invoked even after returning an error")
}
