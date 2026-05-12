package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"booking_monitor/internal/application/booking"
	"booking_monitor/internal/application/worker"
	"booking_monitor/internal/domain"
)

// Session 2 unit tests for the Stage 5 intake consumer. Mirrors the
// saga_consumer_test.go shape:
//
//   - Pure-function tests for the building blocks (decision functions,
//     adapter, error classifier). Cover the operationally meaningful
//     branches exhaustively.
//   - Lifecycle test for the companion idle-reset goroutine ctx-cancel
//     contract. Bounded by a tight timeout so a regression presents as
//     a clean test failure, not a hung suite.
//
// Out of scope (covered by Stage 5 smoke + Session 3 integration):
//
//   - End-to-end FetchMessage → Process → CommitMessages orchestration.
//     The kafka.Reader is a concrete type with no exported test surface;
//     the per-branch logic in handleMessage is small enough that
//     integration coverage is more valuable than a faked-reader unit.

// TestShouldResetLagIntake_TableDriven pins the Stage 5 idle-reset
// decision function. Same matrix as the saga consumer's test —
// keeping the two consumers' tests structurally identical so a future
// contributor can copy one and only change the type name.
func TestShouldResetLagIntake_TableDriven(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	threshold := intakeIdleResetThreshold

	cases := []struct {
		name         string
		lastUnixNano int64
		want         bool
	}{
		{"zero_sentinel_means_reset", 0, true},
		{"just_now_means_dont_reset", now.UnixNano(), false},
		{"1s_ago_means_dont_reset", now.Add(-1 * time.Second).UnixNano(), false},
		{"29s_ago_means_dont_reset", now.Add(-29 * time.Second).UnixNano(), false},
		{"exactly_30s_ago_means_dont_reset", now.Add(-30 * time.Second).UnixNano(), false},
		{"31s_ago_means_reset", now.Add(-31 * time.Second).UnixNano(), true},
		{"1h_ago_means_reset", now.Add(-1 * time.Hour).UnixNano(), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldResetLagIntake(tc.lastUnixNano, now, threshold)
			assert.Equal(t, tc.want, got, tc.name)
		})
	}
}

// TestRunIdleResetIntake_ExitsOnCtxDone pins that the idle-reset
// companion goroutine respects ctx.Done. A regression that forgot the
// ctx.Done case would leak one goroutine per Start invocation;
// `go test -race` would surface it eventually but a tight bounded test
// catches it on the first run.
func TestRunIdleResetIntake_ExitsOnCtxDone(t *testing.T) {
	t.Parallel()

	c := &IntakeConsumer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		c.runIdleReset(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runIdleReset did not exit within 200ms of ctx cancellation — goroutine leak risk")
	}
}

// TestIntakeToQueued_FieldMapping verifies every field of IntakeMessage
// flows into the right field of QueuedBookingMessage. A regression that
// transposed two fields (e.g. UserID ↔ Quantity, AmountCents ↔
// Currency) is the most likely shape of breakage here — covering each
// field with a distinct value catches it.
//
// MessageID is asserted as the exact "p<partition>/o<offset>" string;
// downstream logs use it for operator queries, so the format is a
// contract, not an implementation detail.
//
// ReservedUntil is asserted on the Unix seconds round-trip with UTC
// location — Kafka delivers messages with msg.Time in the broker's
// wall clock, but the QueuedBookingMessage.ReservedUntil is the
// reservation expiry the API handler computed, which we re-derive from
// the intake payload's `reserved_until_unix` int64.
func TestIntakeToQueued_FieldMapping(t *testing.T) {
	t.Parallel()

	orderID := uuid.MustParse("01234567-89ab-7def-0123-456789abcdef")
	eventID := uuid.MustParse("12345678-9abc-7ef0-1234-56789abcdef0")
	ticketTypeID := uuid.MustParse("23456789-abcd-7f01-2345-6789abcdef01")
	reservedAt := time.Date(2026, 5, 13, 18, 30, 0, 0, time.UTC)

	im := booking.IntakeMessage{
		OrderID:       orderID,
		UserID:        42,
		EventID:       eventID,
		TicketTypeID:  ticketTypeID,
		Quantity:      3,
		ReservedUntil: reservedAt.Unix(),
		AmountCents:   54321,
		Currency:      "usd",
	}
	kmsg := kafka.Message{
		Partition: 7,
		Offset:    9999,
	}

	q := intakeToQueued(im, kmsg)
	require.NotNil(t, q)

	assert.Equal(t, "p7/o9999", q.MessageID, "MessageID must be 'p<partition>/o<offset>' for operator log correlation")
	assert.Equal(t, orderID, q.OrderID)
	assert.Equal(t, 42, q.UserID)
	assert.Equal(t, eventID, q.EventID)
	assert.Equal(t, ticketTypeID, q.TicketTypeID)
	assert.Equal(t, 3, q.Quantity)
	assert.True(t, q.ReservedUntil.Equal(reservedAt), "ReservedUntil must be re-derived from reserved_until_unix")
	assert.Equal(t, time.UTC, q.ReservedUntil.Location(), "ReservedUntil location must be UTC for cross-tz consistency")
	assert.Equal(t, int64(54321), q.AmountCents)
	assert.Equal(t, "usd", q.Currency)
}

// TestIsTerminalProcessError_TableDriven pins the classification rule
// that drives the commit-or-redeliver decision. The matrix is the union
// of:
//
//   - domain.IsMalformedOrderInput sentinels (inherited from the
//     Stage 3/4 worker's classifier — must stay in lockstep so all
//     stages give the same answer to the same error)
//   - Stage 5-specific terminal additions: ErrSoldOut /
//     ErrTicketTypeSoldOut / ErrUserAlreadyBought
//   - representative transient errors that MUST remain redeliverable
//
// A regression that drops a sentinel from the terminal set would
// quietly turn it transient — Kafka would redeliver forever and the
// consumer-lag gauge would never recover. The test fails loudly.
func TestIsTerminalProcessError_TableDriven(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Malformed input — terminal per the inherited classifier.
		{"ErrInvalidOrderID", domain.ErrInvalidOrderID, true},
		{"ErrInvalidUserID", domain.ErrInvalidUserID, true},
		{"ErrInvalidEventID", domain.ErrInvalidEventID, true},
		{"ErrInvalidQuantity", domain.ErrInvalidQuantity, true},
		{"ErrInvalidReservedUntil", domain.ErrInvalidReservedUntil, true},
		{"ErrInvalidAmountCents", domain.ErrInvalidAmountCents, true},
		{"ErrInvalidCurrency", domain.ErrInvalidCurrency, true},
		{"ErrInvalidOrderTicketTypeID", domain.ErrInvalidOrderTicketTypeID, true},

		// Stage 5-specific terminal additions.
		{"ErrSoldOut", domain.ErrSoldOut, true},
		{"ErrTicketTypeSoldOut", domain.ErrTicketTypeSoldOut, true},
		{"ErrUserAlreadyBought", domain.ErrUserAlreadyBought, true},

		// Wrapped sentinels live in TestIsTerminalProcessError_WrappedWithFmtErrorf
		// below — errors.Is chain walking is the same logic but the
		// %w-fixture needs a small wrapper type, so it sits in its own
		// table to keep this one literal-friendly.

		// Transient — MUST be classified false so Kafka can redeliver.
		{"ctx_deadline_exceeded", context.DeadlineExceeded, false},
		{"ctx_canceled", context.Canceled, false},
		{"generic_unknown_error", errors.New("postgres: connection refused"), false},
		{"nil_error_is_not_terminal_but_caller_never_calls", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isTerminalProcessError(tc.err)
			assert.Equal(t, tc.want, got, "%s: %v", tc.name, tc.err)
		})
	}
}

// TestIsTerminalProcessError_WrappedWithFmtErrorf covers the
// production-realistic wrap path that wrapErr above can't model
// (errors.New doesn't preserve the underlying sentinel). Kept as a
// separate test so the table above stays simple but the wrap semantics
// are still asserted.
func TestIsTerminalProcessError_WrappedWithFmtErrorf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"%w_ErrTicketTypeSoldOut", fmtWrap(domain.ErrTicketTypeSoldOut), true},
		{"%w_ErrInvalidUserID", fmtWrap(domain.ErrInvalidUserID), true},
		{"%w_generic_transient", fmtWrap(errors.New("db_timeout")), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isTerminalProcessError(tc.err), tc.name)
		})
	}
}

// fmtWrap returns a real %w-wrapped error so the errors.Is chain is
// preserved (which the table-level wrapErr helper can't do because
// the table values would need to be initialized at file-scope with
// fmt.Errorf — Go lets us, but it gets noisy fast).
func fmtWrap(err error) error {
	return errWrapper{inner: err}
}

// errWrapper implements errors.Unwrap so the standard library's
// errors.Is can walk the chain. A handwritten unwrapper rather than
// fmt.Errorf so the file-scope declarations stay literal.
type errWrapper struct{ inner error }

func (e errWrapper) Error() string { return "wrapped: " + e.inner.Error() }
func (e errWrapper) Unwrap() error { return e.inner }

// Compile-time guard that the processor stub satisfies the production
// interface — catches a signature drift on worker.MessageProcessor at
// build time rather than at test runtime.
var _ worker.MessageProcessor = (*stubMessageProcessor)(nil)

// stubMessageProcessor is a hand-rolled stub for worker.MessageProcessor.
// Not currently used by any test (the test plan above deferred
// handleMessage orchestration to integration), but kept here so a future
// contributor adding a handleMessage unit test has the stub ready
// instead of needing to wire generated mocks for an interface that has
// no other consumer in this package.
type stubMessageProcessor struct {
	err error
}

func (s *stubMessageProcessor) Process(_ context.Context, _ *worker.QueuedBookingMessage) error {
	return s.err
}
