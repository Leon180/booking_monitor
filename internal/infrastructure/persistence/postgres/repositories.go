package postgres

import (
	"context"
	"database/sql"

	"booking_monitor/internal/domain"
)

type postgresEventRepository struct {
	db *sql.DB
}

func NewPostgresEventRepository(db *sql.DB) domain.EventRepository {
	return &postgresEventRepository{db: db}
}

func (r *postgresEventRepository) GetByID(ctx context.Context, id int) (*domain.Event, error) {
	// Use FOR UPDATE in transaction? Here we just read.
	// Actually, for booking we need transactional context.
	// For simplicity in this stage, we assume the Service manages the Transaction if needed,
	// but standard sql.DB doesn't propagate Tx without context key or passing Tx explicitly.
	// To stick to clean arch, we often pass Tx via context or have a UnitOfWork.
	// Given the "Direct DB Transaction" requirement of Stage 1, we need to support it.

	// However, the `GetByID` is usually just for display.
	// `DeductInventory` is the one that needs transaction or atomic update.

	query := "SELECT id, name, total_tickets, available_tickets FROM events WHERE id = $1 FOR UPDATE"
	row := r.getExecutor(ctx).QueryRowContext(ctx, query, id)

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

func (r *postgresEventRepository) Create(ctx context.Context, event *domain.Event) error {
	query := "INSERT INTO events (name, total_tickets, available_tickets, version) VALUES ($1, $2, $3, $4) RETURNING id"
	return r.getExecutor(ctx).QueryRowContext(ctx, query, event.Name, event.TotalTickets, event.AvailableTickets, event.Version).Scan(&event.ID)
}

// Update persists the event state.
// We use simple update here, relying on FOR UPDATE in GetByID for concurrency control in UoW.
func (r *postgresEventRepository) Update(ctx context.Context, event *domain.Event) error {
	query := "UPDATE events SET available_tickets = $1 WHERE id = $2"
	_, err := r.getExecutor(ctx).ExecContext(ctx, query, event.AvailableTickets, event.ID)
	return err
}

// DeductInventory - Keeping for backward compatibility or direct atomic usage if not using UoW/Entity pattern strictly
func (r *postgresEventRepository) DeductInventory(ctx context.Context, eventID, quantity int) error {
	query := "UPDATE events SET available_tickets = available_tickets - $2 WHERE id = $1 AND available_tickets >= $2"
	res, err := r.getExecutor(ctx).ExecContext(ctx, query, eventID, quantity)
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

type postgresOrderRepository struct {
	db *sql.DB
}

func NewPostgresOrderRepository(db *sql.DB) domain.OrderRepository {
	return &postgresOrderRepository{db: db}
}

func (r *postgresOrderRepository) Create(ctx context.Context, order *domain.Order) error {
	query := "INSERT INTO orders (event_id, user_id, quantity, status) VALUES ($1, $2, $3, $4) RETURNING id, created_at"
	return r.getExecutor(ctx).QueryRowContext(ctx, query, order.EventID, order.UserID, order.Quantity, order.Status).Scan(&order.ID, &order.CreatedAt)
}

func (r *postgresOrderRepository) ListOrders(ctx context.Context, limit, offset int, status *domain.OrderStatus) ([]*domain.Order, int, error) {
	var total int
	var rows *sql.Rows
	var err error

	if status != nil {
		// Filter by status
		if err := r.getExecutor(ctx).QueryRowContext(ctx, "SELECT count(*) FROM orders WHERE status = $1", status).Scan(&total); err != nil {
			return nil, 0, err
		}

		query := "SELECT id, event_id, user_id, quantity, status, created_at FROM orders WHERE status = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3"
		rows, err = r.getExecutor(ctx).QueryContext(ctx, query, status, limit, offset)
	} else {
		// No filter
		if err := r.getExecutor(ctx).QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&total); err != nil {
			return nil, 0, err
		}

		query := "SELECT id, event_id, user_id, quantity, status, created_at FROM orders ORDER BY created_at DESC LIMIT $1 OFFSET $2"
		rows, err = r.getExecutor(ctx).QueryContext(ctx, query, limit, offset)
	}

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var orders []*domain.Order
	for rows.Next() {
		var o domain.Order
		if err := rows.Scan(&o.ID, &o.EventID, &o.UserID, &o.Quantity, &o.Status, &o.CreatedAt); err != nil {
			return nil, 0, err
		}
		orders = append(orders, &o)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return orders, total, nil
}
