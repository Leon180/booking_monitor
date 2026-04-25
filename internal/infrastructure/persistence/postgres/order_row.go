package postgres

import (
	"time"

	"booking_monitor/internal/domain"
)

// orderRow is the persistence-shaped projection of domain.Order. It
// exists so the database concerns (column types, NULL semantics, scan
// targets) live separately from the domain aggregate. A repo never
// scans into a domain.Order directly — it scans into an orderRow,
// then translates via toDomain. Conversely, repo writes go through
// orderRowFromDomain so the domain.Order is never mutated.
//
// Fields are exported because *sql.Rows.Scan needs addressable
// targets. They're package-private (lowercase struct name) so no
// other package can construct or read them — the row type's surface
// is the row's two helper methods.
type orderRow struct {
	ID        int
	EventID   int
	UserID    int
	Quantity  int
	Status    string
	CreatedAt time.Time
}

// orderColumns is the canonical SELECT column list for orders. Used
// by GetByID + ListOrders so adding a column happens in one place.
// INSERT-side columns (no id / created_at — DB-assigned) stay inline
// because the parameter list shape differs.
const orderColumns = "id, event_id, user_id, quantity, status, created_at"

// rowScanner abstracts *sql.Row and *sql.Rows. Both types expose
// `Scan(dest ...any) error` with identical semantics; defining a
// local interface lets row.scanInto accept either.
type rowScanner interface {
	Scan(dest ...any) error
}

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
		ID:        o.ID,
		EventID:   o.EventID,
		UserID:    o.UserID,
		Quantity:  o.Quantity,
		Status:    string(o.Status),
		CreatedAt: o.CreatedAt,
	}
}
