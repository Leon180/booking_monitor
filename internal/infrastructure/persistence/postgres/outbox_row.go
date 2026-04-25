package postgres

import "booking_monitor/internal/domain"

// outboxRow is the persistence-shaped projection of
// domain.OutboxEvent. See order_row.go for the rationale on the
// row-type pattern.
//
// Note: ProcessedAt is intentionally omitted from the row. The only
// SELECT against this column is `WHERE processed_at IS NULL` in
// ListPending — the value would always be nil if loaded, so we
// don't load it. If a future query needs ProcessedAt, add a
// sql.NullTime field and surface it through toDomain.
type outboxRow struct {
	ID        int
	EventType string
	Payload   []byte
	Status    string
}

const outboxColumns = "id, event_type, payload, status"

func (r *outboxRow) scanInto(s rowScanner) error {
	return s.Scan(&r.ID, &r.EventType, &r.Payload, &r.Status)
}

func (r outboxRow) toDomain() domain.OutboxEvent {
	return domain.OutboxEvent{
		ID:        r.ID,
		EventType: r.EventType,
		Payload:   r.Payload,
		Status:    r.Status,
		// ProcessedAt: not loaded — see type comment above.
	}
}

func outboxRowFromDomain(e domain.OutboxEvent) outboxRow {
	return outboxRow{
		ID:        e.ID,
		EventType: e.EventType,
		Payload:   e.Payload,
		Status:    e.Status,
	}
}
