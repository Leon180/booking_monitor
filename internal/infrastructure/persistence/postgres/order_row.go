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
// rows, hence sql.NullTime — zero-value time on the domain side). D4
// adds PaymentIntentID for Pattern A reservations after /pay (NULL
// before the client initiates payment, hence sql.NullString — empty
// string on the domain side). D4.1 adds TicketTypeID + AmountCents +
// Currency for KKTIX 票種 alignment + price snapshot at book time
// (NULL for legacy rows, hence sql.NullString / sql.NullInt64; on the
// domain side legacy rows read back as uuid.Nil / 0 / "").
type orderRow struct {
	ID              uuid.UUID
	EventID         uuid.UUID
	UserID          int // STAYS int — external user reference
	Quantity        int
	Status          string
	CreatedAt       time.Time
	ReservedUntil   sql.NullTime
	PaymentIntentID sql.NullString
	TicketTypeID    uuid.NullUUID
	AmountCents     sql.NullInt64
	Currency        sql.NullString
}

// orderColumns is the canonical SELECT column list for orders. Used
// by GetByID + ListOrders so adding a column happens in one place.
// D3 adds reserved_until for Pattern A reservations; D4 adds
// payment_intent_id (set by /pay); D4.1 adds ticket_type_id +
// amount_cents + currency (KKTIX 票種 + price snapshot).
const orderColumns = "id, event_id, user_id, quantity, status, created_at, reserved_until, payment_intent_id, ticket_type_id, amount_cents, currency"

func (r *orderRow) scanInto(s rowScanner) error {
	return s.Scan(
		&r.ID, &r.EventID, &r.UserID, &r.Quantity, &r.Status, &r.CreatedAt,
		&r.ReservedUntil, &r.PaymentIntentID,
		&r.TicketTypeID, &r.AmountCents, &r.Currency,
	)
}

func (r orderRow) toDomain() domain.Order {
	var reservedUntil time.Time
	if r.ReservedUntil.Valid {
		reservedUntil = r.ReservedUntil.Time
	}
	var paymentIntentID string
	if r.PaymentIntentID.Valid {
		paymentIntentID = r.PaymentIntentID.String
	}
	var ticketTypeID uuid.UUID
	if r.TicketTypeID.Valid {
		ticketTypeID = r.TicketTypeID.UUID
	}
	var amountCents int64
	if r.AmountCents.Valid {
		amountCents = r.AmountCents.Int64
	}
	var currency string
	if r.Currency.Valid {
		currency = r.Currency.String
	}
	return domain.ReconstructOrder(
		r.ID,
		r.UserID,
		r.EventID,
		ticketTypeID,
		r.Quantity,
		domain.OrderStatus(r.Status),
		r.CreatedAt,
		reservedUntil,
		paymentIntentID,
		amountCents,
		currency,
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
	// Same NULL-on-empty rule for payment_intent_id: NULL until /pay
	// (D4) sets it. Empty-string-on-domain-side, NULL-in-DB symmetry
	// keeps the absence operationally observable
	// (`WHERE payment_intent_id IS NULL` works as expected).
	if o.PaymentIntentID() != "" {
		row.PaymentIntentID = sql.NullString{String: o.PaymentIntentID(), Valid: true}
	}
	// D4.1 columns. uuid.Nil / 0 / "" on the domain side → NULL in DB.
	// New (Pattern A + D4.1) orders set all three; legacy rows leave
	// them NULL so historical SELECTs can `WHERE ticket_type_id IS NULL`
	// to identify pre-D4.1 rows.
	if o.TicketTypeID() != uuid.Nil {
		row.TicketTypeID = uuid.NullUUID{UUID: o.TicketTypeID(), Valid: true}
	}
	if o.AmountCents() > 0 {
		row.AmountCents = sql.NullInt64{Int64: o.AmountCents(), Valid: true}
	}
	if o.Currency() != "" {
		row.Currency = sql.NullString{String: o.Currency(), Valid: true}
	}
	return row
}
