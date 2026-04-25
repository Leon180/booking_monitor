package postgres

import (
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
type outboxRow struct {
	ID        uuid.UUID
	EventType string
	Payload   []byte
	Status    string
}

const outboxColumns = "id, event_type, payload, status"

func (r *outboxRow) scanInto(s rowScanner) error {
	return s.Scan(&r.ID, &r.EventType, &r.Payload, &r.Status)
}

func (r outboxRow) toDomain() domain.OutboxEvent {
	return domain.ReconstructOutboxEvent(r.ID, r.EventType, r.Payload, r.Status, nil)
}

func outboxRowFromDomain(e domain.OutboxEvent) outboxRow {
	return outboxRow{
		ID:        e.ID(),
		EventType: e.EventType(),
		Payload:   e.Payload(),
		Status:    e.Status(),
	}
}
