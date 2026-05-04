package worker

import (
	"booking_monitor/internal/domain"
)

// RetryPolicy decides whether a per-message handler error is
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
type RetryPolicy func(err error) bool

// DefaultRetryPolicy is the policy used by the production
// booking pipeline: malformed-input errors from `domain.NewReservation`
// (Pattern A factory) are deterministic and skip the retry budget;
// everything else is treated as transient and retried. The full set
// of malformed sentinels lives at `domain.IsMalformedOrderInput`:
//   - ErrInvalidOrderID (zero UUID)
//   - ErrInvalidUserID (≤ 0)
//   - ErrInvalidEventID (zero UUID)
//   - ErrInvalidOrderTicketTypeID (zero UUID — D4.1)
//   - ErrInvalidQuantity (≤ 0)
//   - ErrInvalidReservedUntil (zero / past — D3)
//   - ErrInvalidAmountCents (≤ 0 — D4.1)
//   - ErrInvalidCurrency (not 3-letter ASCII — D4.1)
//
// `domain.IsMalformedOrderInput` IS the source of truth for which
// sentinels classify a message as permanently unprocessable; this
// function just inverts it (retryable = NOT malformed).
//
// Note: parseMessage errors at the queue boundary (before the handler
// runs) take a different path — they go straight to DLQ via
// `dlqReasonMalformedParse` without consulting this policy. See
// internal/infrastructure/cache/redis_queue.go::parseMessage.
//
// Rolling-upgrade behaviour (D4.1+): the actual DLQ label depends on
// WHICH half is at the new schema:
//
//   - **New worker, old Lua** (worker deployed first, Lua not yet):
//     in-flight messages from the old Lua are missing
//     ticket_type_id / amount_cents / currency entirely. parseMessage
//     in `infrastructure/cache/redis_queue.go` rejects at the queue
//     boundary with `dlqReasonMalformedParse` BEFORE this function
//     ever sees the error. Operators watching for a rolling-upgrade
//     drain spike should grep `dlq_route_total{reason="malformed_parse"}`,
//     not the classified label.
//
//   - **Old worker, new Lua** (Lua deployed first, worker not yet):
//     the new Lua writes the new fields, the old worker's parseMessage
//     ignores the unknown fields, and `domain.NewOrder` (legacy
//     factory) handles the message normally — no DLQ at all. This is
//     the safer rollout direction.
//
//   - **Both halves new but different versions of the SAME field**:
//     parseMessage parses successfully but `domain.NewReservation` 's
//     invariant (e.g. zero ticket_type_id) trips. THIS path goes
//     through DefaultRetryPolicy → IsMalformedOrderInput → fast-path
//     DLQ with `malformed_classified`. Far less common in practice.
//
// Bottom line: rolling-upgrade DLQ spikes during a D4.1 deploy show up
// as `malformed_parse`, not `malformed_classified`. The label
// `malformed_classified` appears only when the producer side is
// schema-correct but the data is invariant-violating.
func DefaultRetryPolicy() RetryPolicy {
	return func(err error) bool {
		return !domain.IsMalformedOrderInput(err)
	}
}
