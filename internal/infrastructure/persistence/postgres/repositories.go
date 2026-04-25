package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
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
// outside a transaction (no FOR UPDATE). Callers that need to read +
// mutate atomically must use the UoW-aware GetByIDForUpdate variant.
func (r *postgresEventRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	query := "SELECT id, name, total_tickets, available_tickets, version FROM events WHERE id = $1"
	row := r.getExecutor(ctx).QueryRowContext(ctx, query, id)

	var (
		eID                                       uuid.UUID
		name                                      string
		totalTickets, availableTickets, eventVersion int
	)
	if err := row.Scan(&eID, &name, &totalTickets, &availableTickets, &eventVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrEventNotFound
		}
		return nil, fmt.Errorf("eventRepository.GetByID id=%s: %w", id, err)
	}
	out := domain.ReconstructEvent(eID, name, totalTickets, availableTickets, eventVersion)
	return &out, nil
}

// GetByIDForUpdate performs a row-locked read via `SELECT ... FOR UPDATE`.
// MUST be called inside a transaction managed by UnitOfWork.
func (r *postgresEventRepository) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	query := "SELECT id, name, total_tickets, available_tickets, version FROM events WHERE id = $1 FOR UPDATE"
	row := r.getExecutor(ctx).QueryRowContext(ctx, query, id)

	var (
		eID                                       uuid.UUID
		name                                      string
		totalTickets, availableTickets, eventVersion int
	)
	if err := row.Scan(&eID, &name, &totalTickets, &availableTickets, &eventVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrEventNotFound
		}
		return nil, fmt.Errorf("eventRepository.GetByIDForUpdate id=%s: %w", id, err)
	}
	out := domain.ReconstructEvent(eID, name, totalTickets, availableTickets, eventVersion)
	return &out, nil
}

func (r *postgresEventRepository) Create(ctx context.Context, event *domain.Event) error {
	// Factory has assigned the UUID; INSERT writes it. No RETURNING
	// needed — the domain layer is the source of truth for identity now.
	query := "INSERT INTO events (id, name, total_tickets, available_tickets, version) VALUES ($1, $2, $3, $4, $5)"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query,
		event.ID(), event.Name(), event.TotalTickets(), event.AvailableTickets(), event.Version()); err != nil {
		return fmt.Errorf("eventRepository.Create: %w", err)
	}
	return nil
}

// Update persists the event state. Simple update; concurrency control
// happens via FOR UPDATE in GetByIDForUpdate within a UoW transaction.
func (r *postgresEventRepository) Update(ctx context.Context, event *domain.Event) error {
	query := "UPDATE events SET available_tickets = $1 WHERE id = $2"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, event.AvailableTickets(), event.ID()); err != nil {
		return fmt.Errorf("eventRepository.Update id=%s: %w", event.ID(), err)
	}
	return nil
}

type postgresOrderRepository struct {
	db *sql.DB
}

func NewPostgresOrderRepository(db *sql.DB) domain.OrderRepository {
	return &postgresOrderRepository{db: db}
}

func (r *postgresOrderRepository) Create(ctx context.Context, order domain.Order) (domain.Order, error) {
	row := orderRowFromDomain(order)
	// Factory has assigned ID + CreatedAt. INSERT writes them
	// verbatim — no RETURNING needed.
	query := "INSERT INTO orders (id, event_id, user_id, quantity, status, created_at) VALUES ($1, $2, $3, $4, $5, $6)"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query,
		row.ID, row.EventID, row.UserID, row.Quantity, row.Status, row.CreatedAt); err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return domain.Order{}, domain.ErrUserAlreadyBought
		}
		return domain.Order{}, fmt.Errorf("orderRepository.Create: %w", err)
	}
	return row.toDomain(), nil
}

func (r *postgresOrderRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	query := "SELECT " + orderColumns + " FROM orders WHERE id = $1"
	var row orderRow
	if err := row.scanInto(r.getExecutor(ctx).QueryRowContext(ctx, query, id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Order{}, domain.ErrOrderNotFound
		}
		return domain.Order{}, fmt.Errorf("orderRepository.GetByID id=%s: %w", id, err)
	}
	return row.toDomain(), nil
}

func (r *postgresOrderRepository) ListOrders(ctx context.Context, limit, offset int, status *domain.OrderStatus) ([]domain.Order, int, error) {
	var total int
	var rows *sql.Rows
	var err error

	if status != nil {
		if err := r.getExecutor(ctx).QueryRowContext(ctx, "SELECT count(*) FROM orders WHERE status = $1", status).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("orderRepository.ListOrders count (status=%v): %w", *status, err)
		}

		query := "SELECT " + orderColumns + " FROM orders WHERE status = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3"
		rows, err = r.getExecutor(ctx).QueryContext(ctx, query, status, limit, offset)
	} else {
		if err := r.getExecutor(ctx).QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("orderRepository.ListOrders count: %w", err)
		}

		query := "SELECT " + orderColumns + " FROM orders ORDER BY created_at DESC LIMIT $1 OFFSET $2"
		rows, err = r.getExecutor(ctx).QueryContext(ctx, query, limit, offset)
	}

	if err != nil {
		return nil, 0, fmt.Errorf("orderRepository.ListOrders query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var orders []domain.Order
	for rows.Next() {
		var row orderRow
		if err := row.scanInto(rows); err != nil {
			return nil, 0, fmt.Errorf("orderRepository.ListOrders scan: %w", err)
		}
		orders = append(orders, row.toDomain())
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("orderRepository.ListOrders rows: %w", err)
	}

	return orders, total, nil
}

func (r *postgresOrderRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.OrderStatus) error {
	query := "UPDATE orders SET status = $1 WHERE id = $2"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, status, id); err != nil {
		return fmt.Errorf("orderRepository.UpdateStatus id=%s status=%v: %w", id, status, err)
	}
	return nil
}

// DecrementTicket implements atomic inventory deduction using DB constraints
func (r *postgresEventRepository) DecrementTicket(ctx context.Context, eventID uuid.UUID, quantity int) error {
	query := "UPDATE events SET available_tickets = available_tickets - $2 WHERE id = $1 AND available_tickets >= $2"
	res, err := r.getExecutor(ctx).ExecContext(ctx, query, eventID, quantity)
	if err != nil {
		return fmt.Errorf("eventRepository.DecrementTicket exec (event=%s, qty=%d): %w", eventID, quantity, err)
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

// Delete removes an event (compensation when Redis SetInventory fails after DB commit).
func (r *postgresEventRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := "DELETE FROM events WHERE id = $1"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("eventRepository.Delete id=%s: %w", id, err)
	}
	return nil
}

// IncrementTicket restores atomic inventory. Used for Saga compensation.
func (r *postgresEventRepository) IncrementTicket(ctx context.Context, eventID uuid.UUID, quantity int) error {
	query := "UPDATE events SET available_tickets = available_tickets + $2 WHERE id = $1 AND available_tickets + $2 <= total_tickets"
	res, err := r.getExecutor(ctx).ExecContext(ctx, query, eventID, quantity)
	if err != nil {
		return fmt.Errorf("eventRepository.IncrementTicket exec (event=%s, qty=%d): %w", eventID, quantity, err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("eventRepository.IncrementTicket rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("eventRepository.IncrementTicket: event %s not found or exceeds total tickets", eventID)
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

func (r *postgresOutboxRepository) Create(ctx context.Context, event domain.OutboxEvent) (domain.OutboxEvent, error) {
	row := outboxRowFromDomain(event)
	// Factory assigned the UUID; INSERT writes it. No RETURNING.
	query := "INSERT INTO events_outbox (id, event_type, payload, status) VALUES ($1, $2, $3, $4)"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, row.ID, row.EventType, row.Payload, row.Status); err != nil {
		return domain.OutboxEvent{}, fmt.Errorf("outboxRepository.Create type=%s: %w", row.EventType, err)
	}
	// Re-emit via toDomain so any future row-side normalisation is
	// applied; preserves caller's ProcessedAt (which is opaque to the row).
	return row.toDomain(), nil
}

func (r *postgresOutboxRepository) ListPending(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	query := "SELECT " + outboxColumns + ` FROM events_outbox
	          WHERE processed_at IS NULL
	          ORDER BY id ASC
	          LIMIT $1`
	rows, err := r.getExecutor(ctx).QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("outboxRepository.ListPending query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []domain.OutboxEvent
	for rows.Next() {
		var row outboxRow
		if err := row.scanInto(rows); err != nil {
			return nil, fmt.Errorf("outboxRepository.ListPending scan: %w", err)
		}
		events = append(events, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outboxRepository.ListPending rows: %w", err)
	}
	return events, nil
}

func (r *postgresOutboxRepository) MarkProcessed(ctx context.Context, id uuid.UUID) error {
	query := "UPDATE events_outbox SET processed_at = NOW() WHERE id = $1"
	if _, err := r.getExecutor(ctx).ExecContext(ctx, query, id); err != nil {
		return fmt.Errorf("outboxRepository.MarkProcessed id=%s: %w", id, err)
	}
	return nil
}
