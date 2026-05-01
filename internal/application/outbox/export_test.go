package outbox

import "context"

// ProcessBatchForTest exposes the unexported processBatch helper to the
// external application_test package. The external-package layout is
// required because internal/mocks now imports application (for
// application.Repositories), and a same-package test importing mocks
// would create a compile-time cycle. This file is in *_test.go so it
// never compiles into the production binary.
func (r *Relay) ProcessBatchForTest(ctx context.Context) {
	r.processBatch(ctx)
}

// RunWithBatchHookForTest exposes the unexported runWithBatchHook so
// tests can drive the Run-loop's lock-acquisition / ctx-cancellation /
// defer-Unlock branches without timing on a real ticker. The hook
// replaces processBatch with a test-controlled function (counter,
// signal channel) so leader / standby / lock-error paths are
// deterministically exercised.
func (r *Relay) RunWithBatchHookForTest(ctx context.Context, batchFn func(context.Context) error) {
	r.runWithBatchHook(ctx, batchFn)
}

// OutboxLockIDForTest mirrors the unexported outboxLockID constant so
// tests can assert TryLock / Unlock are called with the EXACT advisory
// lock id, not gomock.Any(). The lock id is a correctness invariant —
// all Relay replicas must compete for the same lock or two could both
// become leader and double-publish outbox events. Asserting on this
// value catches arg-swap / typo regressions that gomock.Any() hides.
const OutboxLockIDForTest = outboxLockID
