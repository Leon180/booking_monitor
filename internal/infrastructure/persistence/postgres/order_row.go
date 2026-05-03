package postgres

import (
	"database/sql"
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// orderRow is the persistence-shaped projection of domain.Order. See
// PR 33 for the row-pattern rationale; PR 34 changes ID + EventID
// from int (SERIAL) to uuid.UUID (UUID v7, factory-generated). D3
// adds ReservedUntil for Pattern A reservations (NULL for legacy
// rows, hence sql.NullTime — zero-value time on the domain side).
type orderRow struct {
	ID            uuid.UUID
	EventID       uuid.UUID
	UserID        int // STAYS int — external user reference
	Quantity      int
	Status        string
	CreatedAt     time.Time
	ReservedUntil sql.NullTime
}

// orderColumns is the canonical SELECT column list for orders. Used
// by GetByID + ListOrders so adding a column happens in one place.
// D3 adds reserved_until for Pattern A reservations.
const orderColumns = "id, event_id, user_id, quantity, status, created_at, reserved_until"

func (r *orderRow) scanInto(s rowScanner) error {
	return s.Scan(&r.ID, &r.EventID, &r.UserID, &r.Quantity, &r.Status, &r.CreatedAt, &r.ReservedUntil)
}

func (r orderRow) toDomain() domain.Order {
	var reservedUntil time.Time
	if r.ReservedUntil.Valid {
		reservedUntil = r.ReservedUntil.Time
	}
	return domain.ReconstructOrder(
		r.ID,
		r.UserID,
		r.EventID,
		r.Quantity,
		domain.OrderStatus(r.Status),
		r.CreatedAt,
		reservedUntil,
	)
}

func orderRowFromDomain(o domain.Order) orderRow {
	row := orderRow{
		ID:        o.ID(),
		EventID:   o.EventID(),
		UserID:    o.UserID(),
		Quantity:  o.Quantity(),
		Status:    string(o.Status()),
		CreatedAt: o.CreatedAt(),
	}
	// Only set ReservedUntil for Pattern A orders. Legacy A4 rows
	// have a zero-value reservedUntil — write NULL to the column so
	// the absence is visible in the DB rather than encoded as a magic
	// epoch value.
	if !o.ReservedUntil().IsZero() {
		row.ReservedUntil = sql.NullTime{Time: o.ReservedUntil(), Valid: true}
	}
	return row
}
