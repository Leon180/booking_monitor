package postgres

import (
	"time"

	"github.com/google/uuid"

	"booking_monitor/internal/domain"
)

// orderRow is the persistence-shaped projection of domain.Order. See
// PR 33 for the row-pattern rationale; PR 34 changes ID + EventID
// from int (SERIAL) to uuid.UUID (UUID v7, factory-generated).
type orderRow struct {
	ID        uuid.UUID
	EventID   uuid.UUID
	UserID    int // STAYS int — external user reference
	Quantity  int
	Status    string
	CreatedAt time.Time
}

// orderColumns is the canonical SELECT column list for orders. Used
// by GetByID + ListOrders so adding a column happens in one place.
const orderColumns = "id, event_id, user_id, quantity, status, created_at"

func (r *orderRow) scanInto(s rowScanner) error {
	return s.Scan(&r.ID, &r.EventID, &r.UserID, &r.Quantity, &r.Status, &r.CreatedAt)
}

func (r orderRow) toDomain() domain.Order {
	return domain.ReconstructOrder(
		r.ID,
		r.UserID,
		r.EventID,
		r.Quantity,
		domain.OrderStatus(r.Status),
		r.CreatedAt,
	)
}

func orderRowFromDomain(o domain.Order) orderRow {
	return orderRow{
		ID:        o.ID(),
		EventID:   o.EventID(),
		UserID:    o.UserID(),
		Quantity:  o.Quantity(),
		Status:    string(o.Status()),
		CreatedAt: o.CreatedAt(),
	}
}
