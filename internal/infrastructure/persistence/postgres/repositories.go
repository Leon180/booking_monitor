package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"

	"booking_monitor/internal/domain"
)

type postgresEventRepository struct {
	db *sql.DB
}

func NewPostgresEventRepository(db *sql.DB) domain.EventRepository {
	return &postgresEventRepository{db: db}
}

// GetByID performs a plain read with no locking. It is safe to call
// outside a transaction (no FOR UPDATE). Callers that need to read + mutate
// atomically must use the UoW-aware GetByIDForUpdate variant below.
func (r *postgresEventRepository) GetByID(ctx context.Context, id int) (*domain.Event, error) {
	query := "SELECT id, name, total_tickets, available_tickets FROM events WHERE id = $1"
	row := r.getExecutor(ctx).QueryRowContext(ctx, query, id)

	var event domain.Event
	if err := row.Scan(&event.ID, &event.Name, &event.TotalTickets, &event.AvailableTickets); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrEventNotFound
		}
		return nil, fmt.Errorf("eventRepository.GetByID id=%d: %w", id, err)
	}
	return &event, nil
}

// GetByIDForUpdate performs a row-locked read via `SELECT ... FOR UPDATE`.
// It MUST be called inside a transaction scope managed by the UnitOfWork;
// outside a transaction, Postgres takes the row lock on an auto-commit
// single-row statement and releases it immediately, producing the same
// result as GetByID plus pointless lock overhead. The method is kept on
// the interface so future bugfixes (e.g. the saga compensator taking a
// row lock before IncrementTicket) have a clear, discoverable API.
func (r *postgresEventRepository) GetByIDForUpdate(ctx context.Context, id int) (*domain.Event, error) {
	query := "SELECT id, name, total_tickets, available_tickets FROM events WHERE id = $1 FOR UPDATE"
	row := r.getExecutor(ctx).QueryRowContext(ctx, query, id)

	var event domain.Event
	if err := row.Scan(&event.ID, &event.Name, &event.TotalTickets, &event.AvailableTickets); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrEventNotFound
		}
		return nil, fmt.Errorf("eventRepository.GetByIDForUpdate id=%d: %w", id, err)
	}
	return &event, nil
}

func (r *postgresEventRepository) Create(ctx context.Context, event *domain.Event) error {
	query := "INSERT INTO events (name, total_tickets, available_tickets, version) VALUES ($1, $2, $3, $4) RETURNING id"
	if err := r.getExecutor(ctx).QueryRowContext(ctx, query, event.Name, event.TotalTickets, event.AvailableTickets, event.Version).Scan(&event.ID); err != nil {
		return fmt.Errorf("eventRepository.Create: %w", err)
	}
	return nil
}

// Update persists the event state.
// We use simple update here, relying on FOR UPDATE in GetByID for concurrency control in UoW.
func (r *postgresEventRepository) Update(ctx context.Context, event *domain.Event) error {
	query := "UPDATE events SET available_tickets = $1 WHERE id = $2"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, event.AvailableTickets, event.ID); err != nil {
		return fmt.Errorf("eventRepository.Update id=%d: %w", event.ID, err)
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
	err := r.getExecutor(ctx).QueryRowContext(ctx, query, order.EventID, order.UserID, order.Quantity, order.Status).Scan(&order.ID, &order.CreatedAt)
	if err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return domain.ErrUserAlreadyBought
		}
		return fmt.Errorf("orderRepository.Create: %w", err)
	}
	return nil
}

func (r *postgresOrderRepository) GetByID(ctx context.Context, id int) (*domain.Order, error) {
	query := "SELECT id, event_id, user_id, quantity, status, created_at FROM orders WHERE id = $1"
	var o domain.Order
	err := r.getExecutor(ctx).QueryRowContext(ctx, query, id).Scan(&o.ID, &o.EventID, &o.UserID, &o.Quantity, &o.Status, &o.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrOrderNotFound
		}
		return nil, fmt.Errorf("orderRepository.GetByID id=%d: %w", id, err)
	}
	return &o, nil
}

func (r *postgresOrderRepository) ListOrders(ctx context.Context, limit, offset int, status *domain.OrderStatus) ([]*domain.Order, int, error) {
	var total int
	var rows *sql.Rows
	var err error

	if status != nil {
		// Filter by status
		if err := r.getExecutor(ctx).QueryRowContext(ctx, "SELECT count(*) FROM orders WHERE status = $1", status).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("orderRepository.ListOrders count (status=%v): %w", *status, err)
		}

		query := "SELECT id, event_id, user_id, quantity, status, created_at FROM orders WHERE status = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3"
		rows, err = r.getExecutor(ctx).QueryContext(ctx, query, status, limit, offset)
	} else {
		// No filter
		if err := r.getExecutor(ctx).QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("orderRepository.ListOrders count: %w", err)
		}

		query := "SELECT id, event_id, user_id, quantity, status, created_at FROM orders ORDER BY created_at DESC LIMIT $1 OFFSET $2"
		rows, err = r.getExecutor(ctx).QueryContext(ctx, query, limit, offset)
	}

	if err != nil {
		return nil, 0, fmt.Errorf("orderRepository.ListOrders query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var orders []*domain.Order
	for rows.Next() {
		var o domain.Order
		if err := rows.Scan(&o.ID, &o.EventID, &o.UserID, &o.Quantity, &o.Status, &o.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("orderRepository.ListOrders scan: %w", err)
		}
		orders = append(orders, &o)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("orderRepository.ListOrders rows: %w", err)
	}

	return orders, total, nil
}

func (r *postgresOrderRepository) UpdateStatus(ctx context.Context, id int, status domain.OrderStatus) error {
	query := "UPDATE orders SET status = $1 WHERE id = $2"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, status, id); err != nil {
		return fmt.Errorf("orderRepository.UpdateStatus id=%d status=%v: %w", id, status, err)
	}
	return nil
}

// DecrementTicket implements atomic inventory deduction using DB constraints
func (r *postgresEventRepository) DecrementTicket(ctx context.Context, eventID, quantity int) error {
	query := "UPDATE events SET available_tickets = available_tickets - $2 WHERE id = $1 AND available_tickets >= $2"
	res, err := r.getExecutor(ctx).ExecContext(ctx, query, eventID, quantity)
	if err != nil {
		return fmt.Errorf("eventRepository.DecrementTicket exec (event=%d, qty=%d): %w", eventID, quantity, err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("eventRepository.DecrementTicket rows: %w", err)
	}

	if rowsAffected == 0 {
		return domain.ErrSoldOut
	}

	return nil
}

// Delete removes an event. Used by EventService.CreateEvent as a
// compensating action when the Redis hot-path SetInventory call fails
// after the DB row has been committed.
func (r *postgresEventRepository) Delete(ctx context.Context, id int) error {
	query := "DELETE FROM events WHERE id = $1"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("eventRepository.Delete id=%d: %w", id, err)
	}
	return nil
}

// IncrementTicket restores atomic inventory. Used for Saga compensation.
func (r *postgresEventRepository) IncrementTicket(ctx context.Context, eventID, quantity int) error {
	// Guard against over-increment beyond total_tickets
	query := "UPDATE events SET available_tickets = available_tickets + $2 WHERE id = $1 AND available_tickets + $2 <= total_tickets"
	res, err := r.getExecutor(ctx).ExecContext(ctx, query, eventID, quantity)
	if err != nil {
		return fmt.Errorf("eventRepository.IncrementTicket exec (event=%d, qty=%d): %w", eventID, quantity, err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("eventRepository.IncrementTicket rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("eventRepository.IncrementTicket: event %d not found or exceeds total tickets", eventID)
	}
	return nil
}

type postgresOutboxRepository struct {
	db *sql.DB
}

func NewPostgresOutboxRepository(db *sql.DB) domain.OutboxRepository {
	return &postgresOutboxRepository{db: db}
}

func (r *postgresOutboxRepository) getExecutor(ctx context.Context) DBExecutor {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return r.db
}

func (r *postgresOutboxRepository) Create(ctx context.Context, event *domain.OutboxEvent) error {
	query := "INSERT INTO events_outbox (event_type, payload, status) VALUES ($1, $2, $3) RETURNING id"
	if err := r.getExecutor(ctx).QueryRowContext(ctx, query, event.EventType, event.Payload, event.Status).Scan(&event.ID); err != nil {
		return fmt.Errorf("outboxRepository.Create type=%s: %w", event.EventType, err)
	}
	return nil
}

func (r *postgresOutboxRepository) ListPending(ctx context.Context, limit int) ([]*domain.OutboxEvent, error) {
	query := `SELECT id, event_type, payload, status FROM events_outbox
	          WHERE processed_at IS NULL
	          ORDER BY id ASC
	          LIMIT $1`
	rows, err := r.getExecutor(ctx).QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("outboxRepository.ListPending query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []*domain.OutboxEvent
	for rows.Next() {
		e := &domain.OutboxEvent{}
		if err := rows.Scan(&e.ID, &e.EventType, &e.Payload, &e.Status); err != nil {
			return nil, fmt.Errorf("outboxRepository.ListPending scan: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outboxRepository.ListPending rows: %w", err)
	}
	return events, nil
}

func (r *postgresOutboxRepository) MarkProcessed(ctx context.Context, id int) error {
	query := "UPDATE events_outbox SET processed_at = NOW() WHERE id = $1"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("outboxRepository.MarkProcessed id=%d: %w", id, err)
	}
	return nil
}
