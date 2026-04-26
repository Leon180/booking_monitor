package application

import (
	"booking_monitor/internal/domain"
)

// WorkerRetryPolicy decides whether a per-message handler error is
// worth retrying. Queue infrastructure consults this on every handler
// failure: returning false short-circuits the retry budget and routes
// the message straight to compensation + DLQ; returning true lets the
// budget play out as normal (transient errors get their N attempts).
//
// Lives in application (not domain, not cache) because the decision
// rule IS the business policy:
//
//   - "deterministic invariant violation" → don't retry (DLQ fast path)
//   - "transient downstream blip"          → retry within budget
//
// Domain owns the error sentinels themselves; cache owns the
// mechanism (retry loop + backoff). Application is the layer that
// knows which sentinels mean "permanent" — and knowing about both
// `domain` semantics and `cache` mechanisms is precisely application's
// job.
//
// This decoupling means cache/redis_queue.go has no domain.IsX call
// embedded in its retry loop; the loop just asks the injected policy.
// A future second consumer of the same queue (DLQ replay worker,
// shadow-test pipe) can plug in a different policy without forking
// the queue implementation.
type WorkerRetryPolicy func(err error) bool

// DefaultOrderRetryPolicy is the policy used by the production
// booking pipeline: malformed-input errors from `domain.NewOrder`
// (UserID<=0, zero EventID, non-positive Quantity) are deterministic
// and skip the retry budget; everything else is treated as transient
// and retried.
//
// `domain.IsMalformedOrderInput` IS the source of truth for which
// sentinels classify a message as permanently unprocessable; this
// function just inverts it (retryable = NOT malformed).
func DefaultOrderRetryPolicy() WorkerRetryPolicy {
	return func(err error) bool {
		return !domain.IsMalformedOrderInput(err)
	}
}
