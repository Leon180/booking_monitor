package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/domain"
)

// TestMessageForOutboxEvent_TimeIsCreatedAt is the regression
// tripwire for PR-D12.4's load-bearing data-path: the saga-
// compensation latency histogram measures
// `time.Since(kafka.Message.Time)` on the consumer side, where
// `kafka.Message.Time` MUST equal the originating outbox event's
// `CreatedAt()` (set at order.failed-emit time). Without this
// thread-through the histogram silently observes ~63 billion
// seconds (epoch-since-year-1) on every event, all landing in
// `+Inf` → broken percentile calculations. See the OutboxEvent
// doc + the EventPublisher doc for the full chain.
//
// This test pins the Go-side construction. Broker-side
// preservation of Time is an end-to-end concern verified by the
// live HTTP smoke (Slice 6) + D12.5's harness.
func TestMessageForOutboxEvent_TimeIsCreatedAt(t *testing.T) {
	t.Parallel()

	id, err := uuid.NewV7()
	require.NoError(t, err)
	createdAt := time.Date(2026, 5, 9, 14, 30, 0, 0, time.UTC)
	payload := []byte(`{"order_id":"abc","quantity":1}`)
	event := domain.ReconstructOutboxEvent(id, domain.EventTypeOrderFailed, payload, domain.OutboxStatusPending, createdAt, nil)

	msg := messageForOutboxEvent(event)

	assert.Equal(t, createdAt, msg.Time,
		"kafka.Message.Time MUST equal OutboxEvent.CreatedAt() — load-bearing for the saga compensation latency histogram. A regression here returns the Go zero value, which time.Since interprets as ~63 BILLION seconds, breaking the histogram silently.")
	assert.Equal(t, domain.EventTypeOrderFailed, msg.Topic, "Topic derives from OutboxEvent.EventType()")
	assert.Equal(t, payload, msg.Value, "Value is the OutboxEvent.Payload() bytes verbatim")
}

// TestMessageForOutboxEvent_ZeroCreatedAtSurfaces — defensive
// pin: if a future regression in the OutboxEvent factory or
// row-scan accidentally produces a zero CreatedAt, the broken
// state is observable here as msg.Time.IsZero() rather than
// silently producing a 63-billion-second histogram observation
// downstream. The publisher itself REFUSES to publish such
// events (see TestPublishOutboxEvent_ZeroCreatedAtRejected
// below).
func TestMessageForOutboxEvent_ZeroCreatedAtSurfaces(t *testing.T) {
	t.Parallel()

	id, err := uuid.NewV7()
	require.NoError(t, err)
	// Note: this is the BAD state we want to catch via the
	// integration test. Constructing it directly via Reconstruct
	// (which bypasses factory invariant validation) so the test
	// can assert the failure mode is observable, not silent.
	event := domain.ReconstructOutboxEvent(id, domain.EventTypeOrderFailed, []byte("{}"), domain.OutboxStatusPending, time.Time{}, nil)

	msg := messageForOutboxEvent(event)

	assert.True(t, msg.Time.IsZero(),
		"a zero CreatedAt MUST surface as msg.Time.IsZero() — caller is responsible for validating non-zero before publishing")
}

// TestPublishOutboxEvent_ZeroCreatedAtRejected pins the defense-
// in-depth zero-CreatedAt guard added in PR-D12.4 Slice 0 fixup
// (per silent-failure-hunter C2 finding). A regression that
// removed the guard would let a zero CreatedAt reach the broker
// and silently corrupt every downstream histogram observation.
//
// **INVARIANT**: the zero-CreatedAt check MUST be the FIRST
// operation in `PublishOutboxEvent`, before any access to
// `p.writer`. This test exploits the invariant by constructing
// a publisher with `writer: nil` — if a future refactor moves a
// nil-writer dereference (or any logging that touches
// `p.writer.Addr()`, etc.) above the guard, the test would
// nil-panic instead of returning the error this assertion
// expects. Loud breakage rather than silent bypass.
func TestPublishOutboxEvent_ZeroCreatedAtRejected(t *testing.T) {
	t.Parallel()

	id, err := uuid.NewV7()
	require.NoError(t, err)
	event := domain.ReconstructOutboxEvent(id, domain.EventTypeOrderFailed, []byte("{}"), domain.OutboxStatusPending, time.Time{}, nil)

	// Publisher with nil writer — see invariant above. If guard
	// is the first op, this works; if not, panic-recovery surfaces
	// the regression in the test failure (which is the goal).
	p := &kafkaPublisher{writer: nil}

	err = p.PublishOutboxEvent(context.Background(), event)
	require.Error(t, err, "publisher MUST refuse zero-CreatedAt events to contain blast radius to one failed publish")
	assert.Contains(t, err.Error(), "zero CreatedAt",
		"error message must explicitly identify the zero-CreatedAt cause for ops triage")
	assert.Contains(t, err.Error(), id.String(),
		"error message must include the outbox event id for ops triage")
}
