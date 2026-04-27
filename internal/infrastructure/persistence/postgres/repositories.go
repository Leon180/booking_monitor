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

// dbExecutor is the subset of `*sql.DB` / `*sql.Tx` shared by both. Each
// repo holds one of these as its sole DB-side dependency: a fresh DB
// handle from the pool when the repo is the long-lived "non-tx" copy, or
// a `*sql.Tx` when the repo was produced by `WithTx`. Note: this is the
// PR-35 replacement for the previous ctx-bound tx-injection pattern; the
// silent fallback to `r.db` and the `ctx.Value(txKey{})` lookups are
// gone — there is no longer any way to "pass the wrong ctx" because the
// tx-bound things are concrete parameters, not ctx values.
type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// --- EventRepository ---

type postgresEventRepository struct {
	exec dbExecutor
}

// NewPostgresEventRepository returns the long-lived non-tx repo. The
// concrete type is returned (not the domain interface) so the postgres
// package can also see the WithTx method, which the UnitOfWork uses
// to build per-tx repos. The fx provider in module.go re-exposes this
// under `domain.EventRepository` for application-layer consumers.
func NewPostgresEventRepository(db *sql.DB) *postgresEventRepository {
	return &postgresEventRepository{exec: db}
}

// WithTx returns a copy of the repo bound to the given tx. Same struct
// shape, different executor — the method bodies are oblivious. Inside a
// UnitOfWork.Do closure, every repo passed via Repositories has been
// run through WithTx; outside, callers use the long-lived non-tx copy.
func (r *postgresEventRepository) WithTx(tx *sql.Tx) *postgresEventRepository {
	return &postgresEventRepository{exec: tx}
}

const eventColumns = "id, name, total_tickets, available_tickets, version"

// GetByID performs a plain read with no locking. Safe outside a tx.
// Use GetByIDForUpdate when read+mutate atomicity is required.
func (r *postgresEventRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Event, error) {
	var row eventRow
	if err := row.scanInto(r.exec.QueryRowContext(ctx, "SELECT "+eventColumns+" FROM events WHERE id = $1", id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Event{}, domain.ErrEventNotFound
		}
		return domain.Event{}, fmt.Errorf("eventRepository.GetByID id=%s: %w", id, err)
	}
	return row.toDomain(), nil
}

// GetByIDForUpdate takes a `SELECT ... FOR UPDATE` row lock; MUST run
// inside a UoW transaction (caller bears that responsibility).
func (r *postgresEventRepository) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (domain.Event, error) {
	var row eventRow
	if err := row.scanInto(r.exec.QueryRowContext(ctx, "SELECT "+eventColumns+" FROM events WHERE id = $1 FOR UPDATE", id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Event{}, domain.ErrEventNotFound
		}
		return domain.Event{}, fmt.Errorf("eventRepository.GetByIDForUpdate id=%s: %w", id, err)
	}
	return row.toDomain(), nil
}

func (r *postgresEventRepository) Create(ctx context.Context, event domain.Event) (domain.Event, error) {
	row := eventRowFromDomain(event)
	if _, err := r.exec.ExecContext(ctx,
		"INSERT INTO events ("+eventColumns+") VALUES ($1, $2, $3, $4, $5)",
		row.ID, row.Name, row.TotalTickets, row.AvailableTickets, row.Version); err != nil {
		return domain.Event{}, fmt.Errorf("eventRepository.Create: %w", err)
	}
	return row.toDomain(), nil
}

func (r *postgresEventRepository) Update(ctx context.Context, event domain.Event) error {
	if _, err := r.exec.ExecContext(ctx,
		"UPDATE events SET available_tickets = $1 WHERE id = $2",
		event.AvailableTickets(), event.ID()); err != nil {
		return fmt.Errorf("eventRepository.Update id=%s: %w", event.ID(), err)
	}
	return nil
}

// DecrementTicket atomically deducts inventory. Relies on the partial
// `available_tickets >= $2` predicate — sold-out maps to `rowsAffected==0`.
func (r *postgresEventRepository) DecrementTicket(ctx context.Context, eventID uuid.UUID, quantity int) error {
	res, err := r.exec.ExecContext(ctx,
		"UPDATE events SET available_tickets = available_tickets - $2 WHERE id = $1 AND available_tickets >= $2",
		eventID, quantity)
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

// IncrementTicket restores inventory. Used by saga compensation. The
// `+ $2 <= total_tickets` predicate prevents over-restore.
func (r *postgresEventRepository) IncrementTicket(ctx context.Context, eventID uuid.UUID, quantity int) error {
	res, err := r.exec.ExecContext(ctx,
		"UPDATE events SET available_tickets = available_tickets + $2 WHERE id = $1 AND available_tickets + $2 <= total_tickets",
		eventID, quantity)
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

// Delete removes an event. Used as compensation when Redis SetInventory
// fails after the DB row is committed (event_service.CreateEvent path).
func (r *postgresEventRepository) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := r.exec.ExecContext(ctx, "DELETE FROM events WHERE id = $1", id); err != nil {
		return fmt.Errorf("eventRepository.Delete id=%s: %w", id, err)
	}
	return nil
}

// --- OrderRepository ---

type postgresOrderRepository struct {
	exec dbExecutor
}

func NewPostgresOrderRepository(db *sql.DB) *postgresOrderRepository {
	return &postgresOrderRepository{exec: db}
}

func (r *postgresOrderRepository) WithTx(tx *sql.Tx) *postgresOrderRepository {
	return &postgresOrderRepository{exec: tx}
}

func (r *postgresOrderRepository) Create(ctx context.Context, order domain.Order) (domain.Order, error) {
	row := orderRowFromDomain(order)
	if _, err := r.exec.ExecContext(ctx,
		"INSERT INTO orders (id, event_id, user_id, quantity, status, created_at) VALUES ($1, $2, $3, $4, $5, $6)",
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
	var row orderRow
	if err := row.scanInto(r.exec.QueryRowContext(ctx, "SELECT "+orderColumns+" FROM orders WHERE id = $1", id)); err != nil {
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
		if err := r.exec.QueryRowContext(ctx, "SELECT count(*) FROM orders WHERE status = $1", status).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("orderRepository.ListOrders count (status=%v): %w", *status, err)
		}
		rows, err = r.exec.QueryContext(ctx,
			"SELECT "+orderColumns+" FROM orders WHERE status = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3",
			status, limit, offset)
	} else {
		if err := r.exec.QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("orderRepository.ListOrders count: %w", err)
		}
		rows, err = r.exec.QueryContext(ctx,
			"SELECT "+orderColumns+" FROM orders ORDER BY created_at DESC LIMIT $1 OFFSET $2",
			limit, offset)
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

// transitionStatus is the shared SQL+error-classification body for the
// three typed Mark* methods.
//
// Atomic check-and-set + audit log via a single CTE statement:
//
//   1. UPDATE orders SET status=$1 WHERE id=$2 AND status=$3 RETURNING id
//   2. INSERT INTO order_status_history (order_id, from_status, to_status)
//      SELECT id, $3, $1 FROM the UPDATE's RETURNING set
//
// Postgres executes both clauses in one statement under a single
// snapshot, so EITHER both happen or neither does — no autocommit /
// tx mode dependence, no race window between the state change and
// the audit row, no application-level orchestration required.
//
// `rowsAffected` reflects the INSERT count (which equals the UPDATE's
// returned row count): 1 = transition + audit both succeeded, 0 =
// UPDATE matched nothing (wrong source state OR row missing). We
// disambiguate the 0 case via a follow-up GetByID so callers see the
// precise sentinel — ErrOrderNotFound vs ErrInvalidTransition.
//
// Audit log design rationale lives in
// `deploy/postgres/migrations/000009_order_status_history.up.sql`.
func (r *postgresOrderRepository) transitionStatus(ctx context.Context, id uuid.UUID, from, to domain.OrderStatus) error {
	const sqlStmt = `
		WITH transitioned AS (
			UPDATE orders
			   SET status = $1
			 WHERE id = $2
			   AND status = $3
			RETURNING id
		)
		INSERT INTO order_status_history (order_id, from_status, to_status)
		SELECT id, $3, $1 FROM transitioned`

	res, err := r.exec.ExecContext(ctx, sqlStmt, to, id, from)
	if err != nil {
		return fmt.Errorf("orderRepository.transitionStatus id=%s from=%s to=%s: %w", id, from, to, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("orderRepository.transitionStatus rows: %w", err)
	}
	if rowsAffected == 1 {
		return nil
	}
	// 0 rows: either the order doesn't exist, or its status isn't
	// `from`. Disambiguate so callers see ErrOrderNotFound vs
	// ErrInvalidTransition.
	if _, getErr := r.GetByID(ctx, id); errors.Is(getErr, domain.ErrOrderNotFound) {
		return domain.ErrOrderNotFound
	}
	return fmt.Errorf("orderRepository.transitionStatus id=%s from=%s to=%s: %w", id, from, to, domain.ErrInvalidTransition)
}

// MarkConfirmed atomically transitions Pending → Confirmed. See
// transitionStatus for the error-classification rules and the
// concurrency guarantee (the source-state predicate is in the WHERE
// clause, not a separate SELECT).
func (r *postgresOrderRepository) MarkConfirmed(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusPending, domain.OrderStatusConfirmed)
}

// MarkFailed atomically transitions Pending → Failed (saga path).
func (r *postgresOrderRepository) MarkFailed(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusPending, domain.OrderStatusFailed)
}

// MarkCompensated atomically transitions Failed → Compensated (saga
// completion). NOTE: not Pending→Compensated — the saga compensator
// is invoked AFTER payment failure has already moved the order to
// Failed, so Compensated only follows Failed.
func (r *postgresOrderRepository) MarkCompensated(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusFailed, domain.OrderStatusCompensated)
}

// --- OutboxRepository ---

type postgresOutboxRepository struct {
	exec dbExecutor
}

func NewPostgresOutboxRepository(db *sql.DB) *postgresOutboxRepository {
	return &postgresOutboxRepository{exec: db}
}

func (r *postgresOutboxRepository) WithTx(tx *sql.Tx) *postgresOutboxRepository {
	return &postgresOutboxRepository{exec: tx}
}

func (r *postgresOutboxRepository) Create(ctx context.Context, event domain.OutboxEvent) (domain.OutboxEvent, error) {
	row := outboxRowFromDomain(event)
	if _, err := r.exec.ExecContext(ctx,
		"INSERT INTO events_outbox (id, event_type, payload, status) VALUES ($1, $2, $3, $4)",
		row.ID, row.EventType, row.Payload, row.Status); err != nil {
		return domain.OutboxEvent{}, fmt.Errorf("outboxRepository.Create type=%s: %w", row.EventType, err)
	}
	return row.toDomain(), nil
}

func (r *postgresOutboxRepository) ListPending(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	rows, err := r.exec.QueryContext(ctx,
		"SELECT "+outboxColumns+` FROM events_outbox
		 WHERE processed_at IS NULL
		 ORDER BY id ASC
		 LIMIT $1`, limit)
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
	if _, err := r.exec.ExecContext(ctx, "UPDATE events_outbox SET processed_at = NOW() WHERE id = $1", id); err != nil {
		return fmt.Errorf("outboxRepository.MarkProcessed id=%s: %w", id, err)
	}
	return nil
}
