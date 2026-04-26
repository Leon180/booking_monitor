package postgres

import (
	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// eventRow is the persistence-shaped projection of domain.Event.
// Mirrors orderRow / outboxRow — see PR 33 for the row-pattern
// rationale; eventRow was deferred from PR 33 because EventRepository's
// pre-PR-30 pointer-write-back interface (`Create(ctx, *Event) error`)
// would have produced a half-migrated state. PR 35 lifts that limitation
// by switching EventRepository to value-in / value-out, so eventRow
// can land here alongside the interface migration.
type eventRow struct {
	ID               uuid.UUID
	Name             string
	TotalTickets     int
	AvailableTickets int
	Version          int
}

func (r *eventRow) scanInto(s rowScanner) error {
	return s.Scan(&r.ID, &r.Name, &r.TotalTickets, &r.AvailableTickets, &r.Version)
}

func (r eventRow) toDomain() domain.Event {
	return domain.ReconstructEvent(r.ID, r.Name, r.TotalTickets, r.AvailableTickets, r.Version)
}

func eventRowFromDomain(e domain.Event) eventRow {
	return eventRow{
		ID:               e.ID(),
		Name:             e.Name(),
		TotalTickets:     e.TotalTickets(),
		AvailableTickets: e.AvailableTickets(),
		Version:          e.Version(),
	}
}
