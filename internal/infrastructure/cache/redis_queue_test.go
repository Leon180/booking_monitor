package cache

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/config"
	mlog "booking_monitor/internal/log"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfig builds a fully-populated *config.Config with the same
// defaults cleanenv would apply at production startup. Tests construct
// configs directly so cleanenv's env-default tags never run; without a
// helper, the new cfg.Worker fields would all be zero (StreamReadCount=0
// → Redis "all messages", StreamBlockTimeout=0 → "no block", maxRetries=0
// → loop never runs) and tests would silently break.
func testConfig(workerID string) *config.Config {
	return &config.Config{
		App: config.AppConfig{WorkerID: workerID},
		Redis: config.RedisConfig{
			MaxConsecutiveReadErrors: 30,
			InventoryTTL:             720 * time.Hour,
			IdempotencyTTL:           24 * time.Hour,
		},
		Worker: config.WorkerConfig{
			StreamReadCount:     10,
			StreamBlockTimeout:  2 * time.Second,
			MaxRetries:          3,
			RetryBaseDelay:      100 * time.Millisecond,
			FailureTimeout:      5 * time.Second,
			PendingBlockTimeout: 100 * time.Millisecond,
			ReadErrorBackoff:    1 * time.Second,
		},
	}
}

func TestRedisOrderQueue_EnsureGroup(t *testing.T) {
	// Setup Miniredis
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	queue := NewRedisOrderQueue(rdb, nil, nopLogger, testConfig("worker-1"), worker.NoopQueueMetrics(), nil)

	ctx := context.Background()

	// 1. Create Group (Success)
	err := queue.EnsureGroup(ctx)
	assert.NoError(t, err)

	// Verify Stream and Group Exist
	groups, err := rdb.XInfoGroups(ctx, "orders:stream").Result()
	assert.NoError(t, err)
	assert.Equal(t, 1, len(groups))
	assert.Equal(t, "orders:group", groups[0].Name)

	// 2. Create Group (Already Exists - Idempotent)
	err = queue.EnsureGroup(ctx)
	assert.NoError(t, err)
}

func TestRedisOrderQueue_Subscribe_PELRecovery(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	// No mock needed for happy path
	queue := NewRedisOrderQueue(rdb, nil, nopLogger, testConfig("worker-1"), worker.NoopQueueMetrics(), nil)
	ctx := mlog.NewContext(context.Background(), nopLogger, "")

	// 1. Create Stream & Group
	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")

	// 2. Add a message and claim it (simulate pending). event_id is a
	// UUID string post-PR-34; user_id stays int (external reference).
	// reserved_until added in D3 — Pattern A reservation TTL field that
	// parseMessage requires. ticket_type_id / amount_cents / currency
	// added in D4.1 — KKTIX 票種 + price snapshot, all parseMessage now
	// rejects on absence (the wire format must mirror deduct.lua exactly).
	eventUUID := uuid.New().String()
	orderUUID := uuid.New().String()
	ticketTypeUUID := uuid.New().String()
	id, _ := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "orders:stream",
		Values: map[string]interface{}{
			"order_id":       orderUUID,
			"user_id":        "1",
			"event_id":       eventUUID,
			"quantity":       "1",
			"reserved_until": fmt.Sprintf("%d", time.Now().Add(15*time.Minute).Unix()),
			"ticket_type_id": ticketTypeUUID,
			"amount_cents":   "2000",
			"currency":       "usd",
		},
	}).Result()

	// Claim it (ReadGroup)
	_, _ = rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    "orders:group",
		Consumer: "worker-1",
		Streams:  []string{"orders:stream", ">"},
		Count:    1,
	}).Result()

	// 3. Subscribe (Checks PEL first)
	// We use a context with timeout to stop the infinite loop of Subscribe
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	processedCount := 0
	handler := func(ctx context.Context, msg *worker.QueuedBookingMessage) error {
		processedCount++
		assert.Equal(t, id, msg.MessageID)
		assert.Equal(t, 1, msg.UserID)
		return nil
	}

	// 4. Run Subscribe
	err := queue.Subscribe(ctx, handler)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// 5. Verify processed
	assert.Equal(t, 1, processedCount)

	// 6. Verify Acked (Pending List should be empty)
	// Use background context as the previous ctx is cancelled
	pending, err := rdb.XPending(context.Background(), "orders:stream", "orders:group").Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), pending.Count)
}

func TestRedisOrderQueue_ParseMessage_Error(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	// Truly unrecoverable malformed message (no event_id field) — exercise
	// the malformed_unrecoverable label path. The legacy-revert path is
	// covered separately below.
	inv := &fakeInventoryRevert{}
	metrics := &recordingQueueMetrics{}
	queue := NewRedisOrderQueue(rdb, inv, nopLogger, testConfig("worker-1"), metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")

	// Add MALFORMED message (missing fields, including event_id — so
	// parseLegacyRevertHints CANNOT extract anything).
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "orders:stream",
		Values: map[string]interface{}{"foo": "bar"},
	})

	handlerCalled := false
	handler := func(ctx context.Context, msg *worker.QueuedBookingMessage) error {
		handlerCalled = true
		return nil
	}

	_ = queue.Subscribe(ctx, handler)

	// Handler should NOT be called for malformed message
	assert.False(t, handlerCalled)

	// Should be moved to DLQ
	len, _ := rdb.XLen(context.Background(), "orders:dlq").Result()
	assert.Equal(t, int64(1), len)

	// D4.1 follow-up (Codex P1) — when revert hints unparseable,
	// inventory is NOT reverted (we don't know what to revert) and the
	// label is `malformed_unrecoverable` so ops can spot the inventory
	// leak. Pinning that the metric label is the load-bearing signal.
	assert.Equal(t, 0, inv.revertCallCount,
		"unrecoverable parse-fail (no event_id) MUST NOT call RevertInventory — there's nothing to revert against")
	assert.Equal(t, uint64(1), metrics.dlqRoutes["malformed_unrecoverable"],
		"unrecoverable parse-fail must emit dlqReasonMalformedUnrecoverable so ops can correlate inventory drift")
}

// TestRedisOrderQueue_ParseMessage_LegacyRevertedHints pins the
// rolling-upgrade safety net (Codex P1): a pre-D4.1 stream message
// missing the new fields (ticket_type_id / amount_cents / currency)
// still has event_id + quantity (D3 stable), so parseLegacyRevertHints
// salvages those, RevertInventory restores the Redis decrement, and
// the DLQ label is `malformed_reverted_legacy` (distinct from
// `malformed_unrecoverable` so an expected rolling-upgrade taper
// doesn't page).
func TestRedisOrderQueue_ParseMessage_LegacyRevertedHints(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	inv := &fakeInventoryRevert{}
	metrics := &recordingQueueMetrics{}
	queue := NewRedisOrderQueue(rdb, inv, nopLogger, testConfig("worker-1"), metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")

	// Pre-D4.1 shape: has event_id + quantity, but lacks the three D4.1
	// mandatory fields. parseMessage will reject; handleParseFailure
	// must fall back to revert-via-hints.
	legacyEventID := uuid.New()
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "orders:stream",
		Values: map[string]interface{}{
			"order_id": uuid.New().String(),
			"user_id":  "1",
			"event_id": legacyEventID.String(),
			"quantity": "3",
			// no ticket_type_id / amount_cents / currency / reserved_until
		},
	})

	_ = queue.Subscribe(ctx, func(_ context.Context, _ *worker.QueuedBookingMessage) error {
		t.Fatal("handler must NOT be invoked when parseMessage rejects the message")
		return nil
	})

	// 1. DLQ trace preserved.
	dlqLen, _ := rdb.XLen(context.Background(), "orders:dlq").Result()
	assert.Equal(t, int64(1), dlqLen)

	// 2. RevertInventory was called with the legacy hints.
	assert.Equal(t, 1, inv.revertCallCount, "legacy parse-fail must call RevertInventory exactly once")
	assert.Equal(t, legacyEventID, inv.lastRevertEventID, "RevertInventory must use the event_id extracted from the raw message")
	assert.Equal(t, 3, inv.lastRevertQty, "RevertInventory must use the quantity extracted from the raw message")

	// 3. DLQ label is the legacy-recovery one, not the unrecoverable
	// one. This is the load-bearing operational distinction from the
	// alert side: a `malformed_reverted_legacy` taper is the expected
	// rolling-upgrade shape, while `malformed_unrecoverable` is a
	// producer regression that warrants paging.
	assert.Equal(t, uint64(1), metrics.dlqRoutes["malformed_reverted_legacy"],
		"legacy parse-fail with successful revert must use dlqReasonMalformedRevertedLegacy")
	assert.Equal(t, uint64(0), metrics.dlqRoutes["malformed_unrecoverable"],
		"legacy parse-fail with successful revert MUST NOT emit malformed_unrecoverable")
}

// TestRedisOrderQueue_ParseMessage_LegacyHintsButRevertFails verifies
// the contract that mirrors handleFailure: when legacy hints ARE
// recoverable but RevertInventory itself fails (Redis outage during
// compensation), the message MUST stay in PEL — neither ACK'd nor
// DLQ'd — so the next consumer pass can retry the (idempotent) revert
// once Redis recovers. ACK + DLQ here would permanently leak Redis
// inventory under any transient revert failure (the original Codex P1
// pattern, just from a different entry point).
func TestRedisOrderQueue_ParseMessage_LegacyHintsButRevertFails(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	inv := &fakeInventoryRevert{revertErr: errors.New("redis: ECONNREFUSED")}
	metrics := &recordingQueueMetrics{}
	queue := NewRedisOrderQueue(rdb, inv, nopLogger, testConfig("worker-1"), metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")

	// Legacy-shape message that COULD be reverted, except RevertInventory
	// is going to fail.
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "orders:stream",
		Values: map[string]interface{}{
			"order_id": uuid.New().String(),
			"user_id":  "1",
			"event_id": uuid.New().String(),
			"quantity": "1",
		},
	})

	_ = queue.Subscribe(ctx, func(_ context.Context, _ *worker.QueuedBookingMessage) error {
		t.Fatal("handler must NOT be invoked when parseMessage rejects the message")
		return nil
	})

	// Subscribe loop will re-read the message from the PEL across the
	// 500ms ctx window, so revertCallCount can be > 1. The contract
	// being pinned is "revert was attempted at least once and the
	// failure metric is being emitted" — not the exact retry count
	// (that depends on PEL block timeout + ctx deadline timing).
	assert.GreaterOrEqual(t, inv.revertCallCount, 1, "legacy hints WERE recoverable so RevertInventory was attempted")
	// RevertFailure metric counts every failed attempt.
	assert.GreaterOrEqual(t, metrics.revertFailures, uint64(1),
		"failed RevertInventory must increment revert_failures so the operator alert path fires")

	// CRITICAL: message MUST stay in PEL — no DLQ entry, no DLQ-route
	// metric. Mirrors handleFailure's contract that revert failure
	// leaves the message claimable for the next consumer pass.
	assert.Equal(t, uint64(0), metrics.dlqRoutes["malformed_unrecoverable"],
		"failed revert MUST NOT emit a DLQ-route metric — the message stays in PEL for retry")
	assert.Equal(t, uint64(0), metrics.dlqRoutes["malformed_reverted_legacy"],
		"failed revert MUST NOT emit a DLQ-route metric")
	dlqLen, _ := rdb.XLen(context.Background(), "orders:dlq").Result()
	assert.Equal(t, int64(0), dlqLen,
		"failed revert MUST NOT write to DLQ — leaving the message in PEL is the load-bearing inventory-leak guard")
}

// TestRedisOrderQueue_Subscribe_MalformedFastPath verifies that handler
// errors classified as `domain.IsMalformedOrderInput` short-circuit the
// per-message retry budget (3 attempts × 100ms..300ms backoff) and route
// straight to compensation + DLQ on the first failure. Without the
// fast-path the worker burns ~600ms of backoff per malformed message
// before the inevitable DLQ write — under sustained malformed traffic
// (producer schema bug, ops-side manual XADD, etc.) that backoff piles
// up goroutines and slows DLQ visibility.
func TestRedisOrderQueue_Subscribe_MalformedFastPath(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	// Inventory repo is invoked from handleFailure (RevertInventory).
	// A nil-tolerant happy-path stub is sufficient — we only care that
	// the FIRST attempt's failure routes here, not that compensation
	// is exhaustively exercised (covered elsewhere).
	inv := &fakeInventoryRevert{}
	// Inject DefaultOrderRetryPolicy so the malformed-input fast-path
	// engages — without it the queue uses the always-retry default
	// and the test would observe 3 attempts instead of 1.
	queue := NewRedisOrderQueue(rdb, inv, nopLogger, testConfig("worker-1"), worker.NoopQueueMetrics(), worker.DefaultRetryPolicy())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")

	// Push a structurally-valid stream entry (parseMessage will succeed)
	// — invariant validation happens later, inside the handler. D4.1 added
	// ticket_type_id / amount_cents / currency to the wire format; all
	// three are required by parseMessage so the entry would otherwise
	// fail at the queue boundary (a different code path than the one
	// this test exercises).
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "orders:stream",
		Values: map[string]interface{}{
			"order_id":       uuid.New().String(),
			"user_id":        "1",
			"event_id":       uuid.New().String(),
			"quantity":       "1",
			"reserved_until": fmt.Sprintf("%d", time.Now().Add(15*time.Minute).Unix()),
			"ticket_type_id": uuid.New().String(),
			"amount_cents":   "2000",
			"currency":       "usd",
		},
	})

	// Handler returns a malformed-classified error. Counts attempts so
	// the assertion can distinguish fast-path (1) from full retry budget (3)
	// without depending on wall-clock timing — Subscribe's outer poll loop
	// runs until ctx expires, so total elapsed measures ctx lifetime, not
	// per-message latency.
	var attempts int
	handler := func(_ context.Context, _ *worker.QueuedBookingMessage) error {
		attempts++
		return domain.ErrInvalidUserID
	}

	_ = queue.Subscribe(ctx, handler)

	assert.Equal(t, 1, attempts,
		"malformed-classified error must short-circuit the retry budget — "+
			"without the fast-path attempts would be 3 (one per retry slot)")

	// Compensation ran (RevertInventory called) and DLQ entry written
	// on the FIRST attempt — not after burning the retry budget.
	assert.True(t, inv.reverted, "RevertInventory must run for malformed messages — Redis inventory was deducted upstream")
	dlqLen, _ := rdb.XLen(context.Background(), "orders:dlq").Result()
	assert.Equal(t, int64(1), dlqLen, "malformed message must end up in DLQ on first attempt")
}

// fakeInventoryRevert is a minimal domain.InventoryRepository stub for
// tests that only exercise the revert path. SetInventory / DeductInventory
// are not relevant here and panic if called (keeps the test honest).
//
// The exported fields capture the LAST RevertInventory call so D4.1
// follow-up tests can assert that legacy parse-fail compensation
// reverted the right inventory key + quantity (Codex P1).
type fakeInventoryRevert struct {
	reverted          bool
	revertCallCount   int
	lastRevertEventID uuid.UUID
	lastRevertQty     int
	lastRevertCompID  string
	revertErr         error // nil → success; non-nil → simulate Redis failure
}

func (f *fakeInventoryRevert) SetInventory(_ context.Context, _ uuid.UUID, _ int) error {
	panic("SetInventory not expected in this test")
}
func (f *fakeInventoryRevert) DeductInventory(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID, _ int, _ int, _ time.Time, _ int64, _ string) (bool, error) {
	panic("DeductInventory not expected in this test")
}
func (f *fakeInventoryRevert) RevertInventory(_ context.Context, eventID uuid.UUID, quantity int, compensationID string) error {
	f.reverted = true
	f.revertCallCount++
	f.lastRevertEventID = eventID
	f.lastRevertQty = quantity
	f.lastRevertCompID = compensationID
	return f.revertErr
}
func (f *fakeInventoryRevert) GetInventory(_ context.Context, _ uuid.UUID) (int, bool, error) {
	panic("GetInventory not expected in this test")
}

// TestRedisOrderQueue_Subscribe_NOGROUPRecreatesGroupAndIncrementsMetric
// pins the contract that NOGROUP self-heal:
//   1. recreates the consumer group (so XReadGroup can keep working)
//   2. calls QueueMetrics.RecordConsumerGroupRecreated() — the alert
//      surface that turns this silent failure mode into a paged event
//
// The recreation uses `$` (current end of stream), which silently drops
// messages enqueued before recovery. The metric is the operator's only
// signal that this happened — without RecordConsumerGroupRecreated being
// called, the `ConsumerGroupRecreated` alert never fires and a real
// production FLUSHALL would silently lose data.
func TestRedisOrderQueue_Subscribe_NOGROUPRecreatesGroupAndIncrementsMetric(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()
	metrics := &recordingQueueMetrics{}

	queue := NewRedisOrderQueue(rdb, nil, nopLogger, testConfig("worker-1"), metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Stream + group exist initially.
	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")
	// Simulate FLUSHALL: blow away the stream + group while the worker
	// is about to read. Subscribe will hit NOGROUP on its first
	// XReadGroup, log + recreate, then try again.
	rdb.FlushAll(ctx)

	handler := func(_ context.Context, _ *worker.QueuedBookingMessage) error { return nil }
	_ = queue.Subscribe(ctx, handler) // ctx-deadline exits the loop; we don't care about return

	assert.GreaterOrEqual(t, metrics.consumerGroupRecreated, uint64(1),
		"NOGROUP self-heal MUST call RecordConsumerGroupRecreated — without it, the ConsumerGroupRecreated alert never fires and silent message loss goes unnoticed")

	// Sanity: the recreation should have actually rebuilt the group
	// (otherwise subsequent XReadGroups would keep failing forever).
	groups, err := rdb.XInfoGroups(context.Background(), "orders:stream").Result()
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "orders:group", groups[0].Name)
}

// TestRedisOrderQueue_Subscribe_NOGROUPInProcessPendingAlsoIncrements
// pins the symmetric-metric contract: NOGROUP can surface from EITHER
// the main XReadGroup loop OR `processPending` (the PEL recovery path
// that runs first when Subscribe starts). The metric must fire from
// both. Without this test, a regression that only handles NOGROUP in
// the main loop would leave the PEL-side path silent — exactly the
// silent-failure-hunter HIGH #1 finding addressed by this PR.
func TestRedisOrderQueue_Subscribe_NOGROUPInProcessPendingAlsoIncrements(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()
	metrics := &recordingQueueMetrics{}

	queue := NewRedisOrderQueue(rdb, nil, nopLogger, testConfig("worker-1"), metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Stream + group exist first.
	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")
	// Wipe everything BEFORE Subscribe runs — processPending will be
	// the first to hit the missing group, not the main XReadGroup
	// loop. That's the exact scenario the silent-failure-hunter flagged.
	rdb.FlushAll(ctx)

	handler := func(_ context.Context, _ *worker.QueuedBookingMessage) error { return nil }
	_ = queue.Subscribe(ctx, handler) // ctx-deadline exits the loop

	assert.GreaterOrEqual(t, metrics.consumerGroupRecreated, uint64(1),
		"NOGROUP detected in processPending (PEL recovery) MUST also "+
			"increment the metric — without this, the recovery path "+
			"silently slips past the alert")
}

// recordingQueueMetrics is a deliberately small QueueMetrics impl for
// test-side counter assertions. We don't reach for testify mocks because
// the assertion shape (just "was X called at least N times") is simpler
// inline. Each field is a direct counter.
type recordingQueueMetrics struct {
	xackFailures           uint64
	xaddFailures           map[string]uint64
	revertFailures         uint64
	dlqRoutes              map[string]uint64
	consumerGroupRecreated uint64
}

func (r *recordingQueueMetrics) RecordXAckFailure()         { r.xackFailures++ }
func (r *recordingQueueMetrics) RecordXAddFailure(s string) {
	if r.xaddFailures == nil {
		r.xaddFailures = make(map[string]uint64)
	}
	r.xaddFailures[s]++
}
func (r *recordingQueueMetrics) RecordRevertFailure() { r.revertFailures++ }
func (r *recordingQueueMetrics) RecordDLQRoute(reason string) {
	if r.dlqRoutes == nil {
		r.dlqRoutes = make(map[string]uint64)
	}
	r.dlqRoutes[reason]++
}
func (r *recordingQueueMetrics) RecordConsumerGroupRecreated() {
	r.consumerGroupRecreated++
}

// TestRedisOrderQueue_Subscribe_PersistentErrorBailout verifies that
// Subscribe exits with an error (not loops forever) when the underlying
// Redis is durably unreachable. Previously the loop logged + slept + retried
// indefinitely, leaving the process "alive" to k8s while no messages could
// be consumed. The fix: bounded consecutiveErrors counter → error return.
func TestRedisOrderQueue_Subscribe_PersistentErrorBailout(t *testing.T) {
	s := miniredis.RunT(t)

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	nopLogger := mlog.NewNop()

	queue := NewRedisOrderQueue(rdb, nil, nopLogger, testConfig("worker-1"), worker.NoopQueueMetrics(), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create the stream/group, then close Redis so XReadGroup fails on
	// every iteration. The loop should exit with a wrapped error after
	// maxConsecutiveReadErrors attempts instead of spinning forever.
	rdb.XGroupCreateMkStream(ctx, "orders:stream", "orders:group", "$")
	s.Close()

	handlerCalled := false
	handler := func(ctx context.Context, msg *worker.QueuedBookingMessage) error {
		handlerCalled = true
		return nil
	}

	err := queue.Subscribe(ctx, handler)

	assert.Error(t, err, "Subscribe must return error after persistent Redis failure")
	assert.Contains(t, err.Error(), "XReadGroup")
	assert.Contains(t, err.Error(), "consecutive errors")
	assert.False(t, handlerCalled, "Handler must not fire while Redis is down")
	// ctx had 2 minutes; the bailout should have occurred well before then.
	// If this assert fails, the loop bailed on ctx.Err() not consecutiveErrors.
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
}
