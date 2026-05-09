package postgres

import (
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// outboxRow is the persistence-shaped projection of
// domain.OutboxEvent. See order_row.go for the rationale on the
// row-type pattern.
//
// ProcessedAt is intentionally omitted from the row — only loaded
// queries are `WHERE processed_at IS NULL`, so the value would
// always be nil if loaded.
//
// CreatedAt is read so the domain entity can populate
// `OutboxEvent.CreatedAt()` accurately on rehydration. PR-D12.4
// added this read because the saga-compensation latency histogram
// uses `OutboxEvent.CreatedAt()` as the histogram's start time
// (threaded through `kafka.Message.Time`); pre-D12.4 the field
// existed in the schema (DEFAULT NOW()) but was never read out by
// the row-mapper, so the rehydrated entity carried a zero-value
// CreatedAt that would have broken the histogram if used.
type outboxRow struct {
	ID        uuid.UUID
	EventType string
	Payload   []byte
	Status    string
	CreatedAt time.Time
}

const outboxColumns = "id, event_type, payload, status, created_at"

func (r *outboxRow) scanInto(s rowScanner) error {
	return s.Scan(&r.ID, &r.EventType, &r.Payload, &r.Status, &r.CreatedAt)
}

func (r outboxRow) toDomain() domain.OutboxEvent {
	return domain.ReconstructOutboxEvent(r.ID, r.EventType, r.Payload, r.Status, r.CreatedAt, nil)
}

func outboxRowFromDomain(e domain.OutboxEvent) outboxRow {
	return outboxRow{
		ID:        e.ID(),
		EventType: e.EventType(),
		Payload:   e.Payload(),
		Status:    e.Status(),
		CreatedAt: e.CreatedAt(),
	}
}
