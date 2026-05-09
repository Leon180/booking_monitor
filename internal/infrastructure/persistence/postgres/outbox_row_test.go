package postgres

import (
	"testing"
	"time"

	"booking_monitor/internal/domain"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestOutboxRow_FromDomain_AllFieldsCopied(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	createdAt := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	processedAt := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	e := domain.ReconstructOutboxEvent(id, domain.EventTypeOrderFailed, []byte(`{"id":42}`), domain.OutboxStatusPending, createdAt, &processedAt)

	got := outboxRowFromDomain(e)

	assert.Equal(t, id, got.ID)
	assert.Equal(t, domain.EventTypeOrderFailed, got.EventType)
	assert.Equal(t, []byte(`{"id":42}`), got.Payload)
	assert.Equal(t, domain.OutboxStatusPending, got.Status)
	assert.Equal(t, createdAt, got.CreatedAt, "CreatedAt must round-trip — load-bearing for the saga-compensation latency histogram (PR-D12.4)")
	// ProcessedAt is intentionally NOT in outboxRow — see type comment.
}

func TestOutboxRow_ToDomain_LeavesProcessedAtNil(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	createdAt := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	row := outboxRow{
		ID:        id,
		EventType: domain.EventTypeOrderFailed,
		Payload:   []byte("{}"),
		Status:    domain.OutboxStatusPending,
		CreatedAt: createdAt,
	}

	got := row.toDomain()

	assert.Equal(t, id, got.ID())
	assert.Equal(t, domain.EventTypeOrderFailed, got.EventType())
	assert.Equal(t, domain.OutboxStatusPending, got.Status())
	assert.Equal(t, createdAt, got.CreatedAt(), "CreatedAt must round-trip from PG row → domain entity for the saga-compensation latency histogram")
	assert.Nil(t, got.ProcessedAt(), "ListPending only loads pending rows — ProcessedAt must default to nil after toDomain")
}
