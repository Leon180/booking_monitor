package postgres

import (
	"context"
	"database/sql"

	"booking_monitor/internal/domain"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "infrastructure/persistence/postgres"

type PostgresEventRepository struct {
	db *sql.DB
}

func NewPostgresEventRepository(db *sql.DB) *PostgresEventRepository {
	return &PostgresEventRepository{db: db}
}

func (r *PostgresEventRepository) GetByID(ctx context.Context, id int) (*domain.Event, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "GetByID", trace.WithAttributes(attribute.Int("event_id", id)))
	defer span.End()

	// Use FOR UPDATE in transaction? Here we just read.
	// Actually, for booking we need transactional context.
	// For simplicity in this stage, we assume the Service manages the Transaction if needed,
	// but standard sql.DB doesn't propagate Tx without context key or passing Tx explicitly.
	// To stick to clean arch, we often pass Tx via context or have a UnitOfWork.
	// Given the "Direct DB Transaction" requirement of Stage 1, we need to support it.

	// However, the `GetByID` is usually just for display.
	// `DeductInventory` is the one that needs transaction or atomic update.

	query := "SELECT id, name, total_tickets, available_tickets FROM events WHERE id = $1"
	row := r.db.QueryRowContext(ctx, query, id)

	var event domain.Event
	err := row.Scan(&event.ID, &event.Name, &event.TotalTickets, &event.AvailableTickets)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrEventNotFound
		}
		return nil, err
	}
	return &event, nil
}

// DeductInventory Transactional logic is tricky to abstract perfectly without a Tx manager.
// For this specific 'DeductInventory' method, we can encapsulate the whole atomic update
// OR we can require valid Tx in context.
// Let's go with the single-statement update for simplicity and performance which works for Postgres efficiently.
// "UPDATE events SET available_tickets = available_tickets - 1 WHERE id = $1 AND available_tickets > 0"
func (r *PostgresEventRepository) DeductInventory(ctx context.Context, eventID, quantity int) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "DeductInventory", trace.WithAttributes(
		attribute.Int("event_id", eventID),
		attribute.Int("quantity", quantity),
	))
	defer span.End()

	// Atomic update: only deduct if enough tickets are available.
	// "available_tickets >= $2" ensures we don't oversell.
	query := "UPDATE events SET available_tickets = available_tickets - $2 WHERE id = $1 AND available_tickets >= $2"
	res, err := r.db.ExecContext(ctx, query, eventID, quantity)
	if err != nil {
		return err
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return domain.ErrSoldOut
	}

	return nil
}

type PostgresOrderRepository struct {
	db *sql.DB
}

func NewPostgresOrderRepository(db *sql.DB) *PostgresOrderRepository {
	return &PostgresOrderRepository{db: db}
}

func (r *PostgresOrderRepository) Create(ctx context.Context, order *domain.Order) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "CreateOrder", trace.WithAttributes(attribute.Int("user_id", order.UserID)))
	defer span.End()

	query := "INSERT INTO orders (event_id, user_id, quantity, status) VALUES ($1, $2, $3, $4) RETURNING id, created_at"
	return r.db.QueryRowContext(ctx, query, order.EventID, order.UserID, order.Quantity, order.Status).Scan(&order.ID, &order.CreatedAt)
}
