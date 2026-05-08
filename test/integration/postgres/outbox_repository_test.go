//go:build integration

package pgintegration_test

import (
	"context"
	"fmt"
	"testing"

	"booking_monitor/internal/domain"
	"booking_monitor/internal/infrastructure/persistence/postgres"
	pgintegration "booking_monitor/test/integration/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration tests for postgresOutboxRepository against a real
// postgres:15-alpine container.
//
// Load-bearing contracts pinned (none reachable via row-mapper unit
// tests):
//
//   - The `WHERE processed_at IS NULL` predicate in ListPending. The
//     transactional outbox pattern's correctness depends on this:
//     processed events MUST be invisible to subsequent ListPending
//     calls. A regression that dropped the predicate would cause
//     re-publication of every event on every relay tick.
//
//   - The `ORDER BY id ASC` ordering. Outbox events use UUIDv7 ids
//     which are time-prefixed, so id-ASC is roughly chronological.
//     A regression that reordered ListPending could publish events
//     out of order, breaking saga compensators that expect causal
//     ordering.
//
//   - MarkProcessed idempotency under double-call (the relay's at-
//     least-once delivery guarantees us a dup-MarkProcessed in some
//     failure modes; second call must be a no-op, not an error).
//
//   - The partial index from migration 000007 (covered separately
//     by harness_test.go's index-existence smoke test).

func outboxRepoHarness(t *testing.T) (*pgintegration.Harness, domain.OutboxRepository) {
	t.Helper()
	h := pgintegration.StartPostgres(context.Background(), t)
	repo := postgres.NewPostgresOutboxRepository(h.DB)
	return h, repo
}

func newOutboxEvent(t *testing.T, payload []byte) domain.OutboxEvent {
	t.Helper()
	ev, err := domain.NewOrderFailedOutbox(payload)
	require.NoError(t, err)
	return ev
}

// TestOutboxRepository_CreateAndListPending: round-trip an outbox
// event via Create + ListPending. Pins that fresh events appear in
// ListPending with their original payload + event_type.
func TestOutboxRepository_CreateAndListPending(t *testing.T) {
	h, repo := outboxRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newOutboxEvent(t, []byte(`{"order_id":"abc"}`))

	created, err := repo.Create(ctx, ev)
	require.NoError(t, err)
	assert.Equal(t, ev.ID(), created.ID(),
		"Create must return the same id passed in (caller-generated)")

	pending, err := repo.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, ev.ID(), pending[0].ID())
	assert.Equal(t, domain.EventTypeOrderFailed, pending[0].EventType())
	assert.JSONEq(t, `{"order_id":"abc"}`, string(pending[0].Payload()))
}

// TestOutboxRepository_ListPending_FiltersProcessed: events with
// processed_at NOT NULL must be invisible to ListPending. Pins the
// `WHERE processed_at IS NULL` predicate — load-bearing for the
// transactional outbox pattern (otherwise the relay would re-publish
// every event on every tick).
func TestOutboxRepository_ListPending_FiltersProcessed(t *testing.T) {
	h, repo := outboxRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()

	// Create two events.
	ev1 := newOutboxEvent(t, []byte(`{"n":1}`))
	ev2 := newOutboxEvent(t, []byte(`{"n":2}`))
	_, err := repo.Create(ctx, ev1)
	require.NoError(t, err)
	_, err = repo.Create(ctx, ev2)
	require.NoError(t, err)

	// Mark ev1 processed. ListPending must now return only ev2.
	require.NoError(t, repo.MarkProcessed(ctx, ev1.ID()))

	pending, err := repo.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1, "processed events MUST be excluded — pins the WHERE processed_at IS NULL predicate")
	assert.Equal(t, ev2.ID(), pending[0].ID())
}

// TestOutboxRepository_ListPending_OrderingIsIdAsc: events are
// returned in id ASC order. Pins the ORDER BY clause — UUIDv7 ids
// are time-prefixed so id-ASC is roughly chronological, which saga
// compensators rely on for causal ordering.
//
// We create events in alternating "newer first, older second" order
// to verify the SQL sort, not just insertion order.
func TestOutboxRepository_ListPending_OrderingIsIdAsc(t *testing.T) {
	h, repo := outboxRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()

	// uuid.NewV7 is monotonic by clock; we generate 3 ids in advance
	// and insert them in non-monotonic order to exercise the sort.
	id1, err := uuid.NewV7()
	require.NoError(t, err)
	id2, err := uuid.NewV7()
	require.NoError(t, err)
	id3, err := uuid.NewV7()
	require.NoError(t, err)

	// Build outbox events with these specific ids via the wire-shape
	// reconstruction helper (bypasses the factory's auto-id).
	// fmt.Sprintf instead of string(rune('0'+n)) — the latter
	// produces ":" for n=10 and other non-digit runes for n>9.
	payloadFor := func(n int) []byte { return []byte(fmt.Sprintf(`{"n":%d}`, n)) }
	ev1 := domain.ReconstructOutboxEvent(id1, domain.EventTypeOrderFailed, payloadFor(1), domain.OutboxStatusPending, nil)
	ev2 := domain.ReconstructOutboxEvent(id2, domain.EventTypeOrderFailed, payloadFor(2), domain.OutboxStatusPending, nil)
	ev3 := domain.ReconstructOutboxEvent(id3, domain.EventTypeOrderFailed, payloadFor(3), domain.OutboxStatusPending, nil)

	// Insert in REVERSE order (3, 1, 2) — sort must override.
	_, err = repo.Create(ctx, ev3)
	require.NoError(t, err)
	_, err = repo.Create(ctx, ev1)
	require.NoError(t, err)
	_, err = repo.Create(ctx, ev2)
	require.NoError(t, err)

	pending, err := repo.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	assert.Equal(t, id1, pending[0].ID(), "ListPending must sort by id ASC, not by insertion order")
	assert.Equal(t, id2, pending[1].ID())
	assert.Equal(t, id3, pending[2].ID())
}

// TestOutboxRepository_ListPending_LimitRespected: limit caps the
// result. Pins the LIMIT $1 clause; without it, a backlog spike
// could produce a giant allocation in the relay.
func TestOutboxRepository_ListPending_LimitRespected(t *testing.T) {
	h, repo := outboxRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		ev := newOutboxEvent(t, []byte(`{}`))
		_, err := repo.Create(ctx, ev)
		require.NoError(t, err)
	}

	pending, err := repo.ListPending(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, pending, 2,
		"LIMIT must cap the result regardless of how many pending events exist")
}

// TestOutboxRepository_MarkProcessed_Idempotent: the relay's
// at-least-once delivery means the same event id may be passed to
// MarkProcessed twice (a Kafka publish that succeeded but whose
// MarkProcessed was lost will retry). The second call must be a
// no-op, not an error.
func TestOutboxRepository_MarkProcessed_Idempotent(t *testing.T) {
	h, repo := outboxRepoHarness(t)
	h.Reset(t)

	ctx := context.Background()
	ev := newOutboxEvent(t, []byte(`{}`))
	_, err := repo.Create(ctx, ev)
	require.NoError(t, err)

	require.NoError(t, repo.MarkProcessed(ctx, ev.ID()))
	require.NoError(t, repo.MarkProcessed(ctx, ev.ID()),
		"second MarkProcessed must be a no-op — relay's at-least-once delivery requires idempotency")

	// Event must still be excluded from ListPending.
	pending, err := repo.ListPending(ctx, 100)
	require.NoError(t, err)
	assert.Empty(t, pending)

	// Direct column-level assertion that processed_at is non-NULL.
	// The ListPending exclusion above is the indirect signal; this
	// directly checks the column was actually written. Catches a
	// hypothetical regression where MarkProcessed updated a different
	// column (or no column at all) but ListPending's WHERE clause
	// happened to filter the row out via some other path.
	var processedAtIsSet bool
	err = h.DB.QueryRowContext(ctx,
		`SELECT processed_at IS NOT NULL FROM events_outbox WHERE id = $1`,
		ev.ID()).Scan(&processedAtIsSet)
	require.NoError(t, err)
	assert.True(t, processedAtIsSet,
		"MarkProcessed must persist processed_at = NOW() — the load-bearing column write that ListPending's predicate keys on")
}

// TestOutboxRepository_MarkProcessed_NonExistent: marking a
// never-existed id must not error. Defensive — relay code calls
// MarkProcessed via the event id from a previous ListPending result,
// so this should never happen in practice, but the no-error contract
// makes the relay implementation simpler.
//
// No h.Reset(t) — fresh container, no fixture needed.
func TestOutboxRepository_MarkProcessed_NonExistent(t *testing.T) {
	_, repo := outboxRepoHarness(t)

	require.NoError(t, repo.MarkProcessed(context.Background(), uuid.New()),
		"MarkProcessed of non-existent id must not error — UPDATE with no matching row is a no-op, not a failure")
}
