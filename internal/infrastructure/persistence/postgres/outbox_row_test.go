package postgres

import (
	"testing"
	"time"

	"booking_monitor/internal/domain"

	"github.com/stretchr/testify/assert"
)

func TestOutboxRow_FromDomain_AllFieldsCopied(t *testing.T) {
	t.Parallel()

	processedAt := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	e := domain.OutboxEvent{
		ID:          7,
		EventType:   domain.EventTypeOrderCreated,
		Payload:     []byte(`{"id":42}`),
		Status:      domain.OutboxStatusPending,
		ProcessedAt: &processedAt,
	}

	got := outboxRowFromDomain(e)

	assert.Equal(t, 7, got.ID)
	assert.Equal(t, domain.EventTypeOrderCreated, got.EventType)
	assert.Equal(t, []byte(`{"id":42}`), got.Payload)
	assert.Equal(t, domain.OutboxStatusPending, got.Status)
	// ProcessedAt is intentionally NOT in outboxRow — see type comment.
	// This test exists to reaffirm the contract (and to catch a future
	// well-meaning addition that re-introduces it without thinking
	// through the WHERE processed_at IS NULL semantics).
}

func TestOutboxRow_ToDomain_LeavesProcessedAtNil(t *testing.T) {
	t.Parallel()

	row := outboxRow{
		ID:        1,
		EventType: domain.EventTypeOrderCreated,
		Payload:   []byte("{}"),
		Status:    domain.OutboxStatusPending,
	}

	got := row.toDomain()

	assert.Equal(t, 1, got.ID)
	assert.Equal(t, domain.EventTypeOrderCreated, got.EventType)
	assert.Equal(t, domain.OutboxStatusPending, got.Status)
	assert.Nil(t, got.ProcessedAt, "ListPending only loads pending rows — ProcessedAt must default to nil after toDomain")
}
