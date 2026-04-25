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
	processedAt := time.Date(2026, 4, 25, 10, 30, 0, 0, time.UTC)
	e := domain.ReconstructOutboxEvent(id, domain.EventTypeOrderCreated, []byte(`{"id":42}`), domain.OutboxStatusPending, &processedAt)

	got := outboxRowFromDomain(e)

	assert.Equal(t, id, got.ID)
	assert.Equal(t, domain.EventTypeOrderCreated, got.EventType)
	assert.Equal(t, []byte(`{"id":42}`), got.Payload)
	assert.Equal(t, domain.OutboxStatusPending, got.Status)
	// ProcessedAt is intentionally NOT in outboxRow — see type comment.
}

func TestOutboxRow_ToDomain_LeavesProcessedAtNil(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	row := outboxRow{
		ID:        id,
		EventType: domain.EventTypeOrderCreated,
		Payload:   []byte("{}"),
		Status:    domain.OutboxStatusPending,
	}

	got := row.toDomain()

	assert.Equal(t, id, got.ID())
	assert.Equal(t, domain.EventTypeOrderCreated, got.EventType())
	assert.Equal(t, domain.OutboxStatusPending, got.Status())
	assert.Nil(t, got.ProcessedAt(), "ListPending only loads pending rows — ProcessedAt must default to nil after toDomain")
}
