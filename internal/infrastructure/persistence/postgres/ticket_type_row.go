package postgres

import (
	"database/sql"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// ticketTypeRow is the persistence-shaped projection of
// domain.TicketType. Mirrors eventRow / orderRow / outboxRow — see
// PR 33 for the row-pattern rationale; D4.1 (this PR) introduces the
// aggregate.
//
// SQL NULL handling: `sale_starts_at` / `sale_ends_at` / `per_user_limit`
// / `area_label` are nullable per migration 000014. The domain side
// uses pointer types (*time.Time / *int) for the first three to
// preserve the "operator did/did-not set this field" distinction;
// area_label uses empty-string-as-absent (string instead of *string)
// to match the project's existing convention for VARCHAR fields where
// `""` is operationally equivalent to NULL (the field is display-only,
// no business logic branches on it).
//
// NULL-vs-empty-string convention for area_label (operational note):
// the application layer always writes NULL when the domain has `""`,
// and reads NULL → `""`. If an admin / direct-SQL tool inserts
// `area_label = ''` (not NULL), `toDomain` reads it back as `""` —
// behaviorally identical from the domain's perspective. BUT a
// reporting query like `WHERE area_label IS NULL` would miss those
// `''` rows. If you need "all rows with no area assigned", the
// canonical predicate is `WHERE area_label IS NULL OR area_label = ''`.
type ticketTypeRow struct {
	ID               uuid.UUID
	EventID          uuid.UUID
	Name             string
	PriceCents       int64
	Currency         string
	TotalTickets     int
	AvailableTickets int
	SaleStartsAt     sql.NullTime
	SaleEndsAt       sql.NullTime
	PerUserLimit     sql.NullInt32
	AreaLabel        sql.NullString
	Version          int
}

// ticketTypeColumns is the canonical SELECT column list. Centralised
// so adding a column happens in one place — same convention as
// orderColumns / eventColumns.
const ticketTypeColumns = "id, event_id, name, price_cents, currency, total_tickets, available_tickets, sale_starts_at, sale_ends_at, per_user_limit, area_label, version"

func (r *ticketTypeRow) scanInto(s rowScanner) error {
	return s.Scan(
		&r.ID, &r.EventID, &r.Name, &r.PriceCents, &r.Currency,
		&r.TotalTickets, &r.AvailableTickets,
		&r.SaleStartsAt, &r.SaleEndsAt,
		&r.PerUserLimit, &r.AreaLabel,
		&r.Version,
	)
}

func (r ticketTypeRow) toDomain() domain.TicketType {
	var saleStartsAt *time.Time
	if r.SaleStartsAt.Valid {
		t := r.SaleStartsAt.Time
		saleStartsAt = &t
	}
	var saleEndsAt *time.Time
	if r.SaleEndsAt.Valid {
		t := r.SaleEndsAt.Time
		saleEndsAt = &t
	}
	var perUserLimit *int
	if r.PerUserLimit.Valid {
		v := int(r.PerUserLimit.Int32)
		perUserLimit = &v
	}
	var areaLabel string
	if r.AreaLabel.Valid {
		areaLabel = r.AreaLabel.String
	}
	return domain.ReconstructTicketType(
		r.ID, r.EventID, r.Name,
		r.PriceCents, r.Currency,
		r.TotalTickets, r.AvailableTickets,
		saleStartsAt, saleEndsAt,
		perUserLimit, areaLabel,
		r.Version,
	)
}

func ticketTypeRowFromDomain(t domain.TicketType) ticketTypeRow {
	areaLabel := t.AreaLabel()
	row := ticketTypeRow{
		ID:               t.ID(),
		EventID:          t.EventID(),
		Name:             t.Name(),
		PriceCents:       t.PriceCents(),
		Currency:         t.Currency(),
		TotalTickets:     t.TotalTickets(),
		AvailableTickets: t.AvailableTickets(),
		AreaLabel:        sql.NullString{String: areaLabel, Valid: areaLabel != ""},
		Version:          t.Version(),
	}
	if v := t.SaleStartsAt(); v != nil {
		row.SaleStartsAt = sql.NullTime{Time: *v, Valid: true}
	}
	if v := t.SaleEndsAt(); v != nil {
		row.SaleEndsAt = sql.NullTime{Time: *v, Valid: true}
	}
	if v := t.PerUserLimit(); v != nil {
		row.PerUserLimit = sql.NullInt32{Int32: int32(*v), Valid: true}
	}
	return row
}
