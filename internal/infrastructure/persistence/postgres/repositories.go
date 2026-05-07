package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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

// ListAvailable returns events with `available_tickets > 0`, ordered
// newest-first. Used by app-startup inventory rehydrate (see
// `cache.RehydrateInventory`) to repopulate Redis qty keys after
// Redis is wiped or freshly deployed. No `LIMIT` — at our scale,
// "active flash-sale events" is a small set; if this ever becomes
// hot enough to matter, paginate.
func (r *postgresEventRepository) ListAvailable(ctx context.Context) ([]domain.Event, error) {
	rows, err := r.exec.QueryContext(ctx,
		"SELECT "+eventColumns+" FROM events WHERE available_tickets > 0 ORDER BY id DESC")
	if err != nil {
		return nil, fmt.Errorf("eventRepository.ListAvailable query: %w", err)
	}
	defer func() { _ = rows.Close() }() // close error is non-actionable; iter err already checked below

	var out []domain.Event
	for rows.Next() {
		var row eventRow
		if err := row.scanInto(rows); err != nil {
			return nil, fmt.Errorf("eventRepository.ListAvailable scan: %w", err)
		}
		out = append(out, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventRepository.ListAvailable iter: %w", err)
	}
	return out, nil
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
		"INSERT INTO orders (id, event_id, user_id, quantity, status, created_at, reserved_until, payment_intent_id, ticket_type_id, amount_cents, currency) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)",
		row.ID, row.EventID, row.UserID, row.Quantity, row.Status, row.CreatedAt, row.ReservedUntil, row.PaymentIntentID, row.TicketTypeID, row.AmountCents, row.Currency); err != nil {
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
//
// A4 changes:
//   - signature accepts variadic `from` source statuses (was: single
//     source). Makes the transitional widening (Pending OR Charging
//     for MarkConfirmed/MarkFailed) explicit in the function shape
//     rather than hidden in a SQL string.
//   - SET also writes `updated_at = NOW()` so the reconciler's
//     `WHERE updated_at < NOW() - threshold` predicate sees fresh
//     timestamps for every transition. Missing this on any single
//     Mark* method silently makes orders invisible to the sweep —
//     the variadic + tests guard against drift.
//   - Audit log's `from_status` is read via a `before` CTE that takes
//     a row-level lock and reads the OLD status, so the audit row is
//     accurate even when the WHERE accepts multiple source values.
//     A naive hardcoded `from = $3` would produce wrong audit rows
//     when the actual source was the second variadic element.
func (r *postgresOrderRepository) transitionStatus(ctx context.Context, id uuid.UUID, to domain.OrderStatus, from ...domain.OrderStatus) error {
	if len(from) == 0 {
		return fmt.Errorf("orderRepository.transitionStatus: at least one `from` status required")
	}

	// Convert []OrderStatus to []string for pq.Array. lib/pq's array
	// support requires the underlying type to be a built-in.
	fromStrings := make([]string, len(from))
	for i, s := range from {
		fromStrings[i] = string(s)
	}

	const sqlStmt = `
		WITH before AS (
			-- FOR UPDATE so the source status read happens under the
			-- same row-level lock the UPDATE will hold; rules out a
			-- concurrent transition between the read and the update.
			SELECT id, status FROM orders WHERE id = $2 FOR UPDATE
		),
		transitioned AS (
			UPDATE orders
			   SET status = $1, updated_at = NOW()
			  FROM before
			 WHERE orders.id = before.id
			   AND before.status = ANY($3)
			RETURNING orders.id, before.status AS from_status
		)
		INSERT INTO order_status_history (order_id, from_status, to_status)
		SELECT id, from_status, $1 FROM transitioned`

	res, err := r.exec.ExecContext(ctx, sqlStmt, to, id, pq.Array(fromStrings))
	if err != nil {
		return fmt.Errorf("orderRepository.transitionStatus id=%s from=%v to=%s: %w", id, from, to, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("orderRepository.transitionStatus rows: %w", err)
	}
	if rowsAffected == 1 {
		return nil
	}
	// 0 rows: either the order doesn't exist, or its status isn't in
	// `from`. Disambiguate so callers see ErrOrderNotFound vs
	// ErrInvalidTransition.
	//
	// The disambiguating GetByID itself can fail (DB outage, ctx
	// cancelled mid-recon-sweep). PRIOR REGRESSION: this code used
	// to silently fall through to ErrInvalidTransition on any
	// non-ErrOrderNotFound error, which masked real DB outages as
	// benign race-loss. The reconciler then counted those as
	// "transition_lost" (Info-level) instead of incrementing
	// recon_mark_errors_total → no alert fired during a real
	// outage. Now we surface the GetByID failure verbatim.
	_, getErr := r.GetByID(ctx, id)
	if errors.Is(getErr, domain.ErrOrderNotFound) {
		return domain.ErrOrderNotFound
	}
	if getErr != nil {
		return fmt.Errorf("orderRepository.transitionStatus id=%s disambiguate via GetByID: %w", id, getErr)
	}
	// GetByID succeeded but the row's status isn't in `from` → genuine
	// invalid-transition (e.g. recon raced the worker and the worker
	// already wrote Confirmed; we tried to MarkConfirmed from Charging
	// but row is now Confirmed).
	return fmt.Errorf("orderRepository.transitionStatus id=%s from=%v to=%s: %w", id, from, to, domain.ErrInvalidTransition)
}

// FindStuckCharging returns Charging-state orders older than minAge,
// up to limit rows. Backs the reconciler subcommand's sweep.
//
// Query design:
//   - Hits the partial index `idx_orders_status_updated_at_partial`
//     created in migration 000010. Index is on (status, updated_at)
//     WHERE status IN ('charging', 'pending'), so the sweep is an
//     index-only or index-range scan even on large tables.
//   - `EXTRACT(EPOCH FROM ...)` gives the age in fractional seconds;
//     scanned into a float64 then converted to time.Duration. Avoids
//     pushing duration semantics into SQL.
//   - ORDER BY updated_at ASC: oldest first. Pairs with batch-size
//     limit to ensure stuck-est orders resolve before fresh ones,
//     even under sustained backlog.
//   - No FOR UPDATE: the subsequent gateway.GetStatus + Mark{Confirmed,Failed}
//     each carry their own row-level lock; holding a SELECT lock
//     across the gateway call would block other workers needlessly.
func (r *postgresOrderRepository) FindStuckCharging(
	ctx context.Context, minAge time.Duration, limit int,
) ([]domain.StuckCharging, error) {
	const sqlStmt = `
		SELECT id, EXTRACT(EPOCH FROM (NOW() - updated_at))
		  FROM orders
		 WHERE status = 'charging'
		   AND updated_at < NOW() - $1::interval
		 ORDER BY updated_at ASC
		 LIMIT $2`

	// $1 is interval-typed; pass as a Postgres interval literal string.
	// time.Duration → seconds → "X seconds" — Postgres parses that as
	// an interval.
	intervalLit := fmt.Sprintf("%f seconds", minAge.Seconds())

	rows, err := r.exec.QueryContext(ctx, sqlStmt, intervalLit, limit)
	if err != nil {
		return nil, fmt.Errorf("orderRepository.FindStuckCharging: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.StuckCharging
	for rows.Next() {
		var id uuid.UUID
		var ageSeconds float64
		if err := rows.Scan(&id, &ageSeconds); err != nil {
			return nil, fmt.Errorf("orderRepository.FindStuckCharging scan: %w", err)
		}
		out = append(out, domain.StuckCharging{
			ID:  id,
			Age: time.Duration(ageSeconds * float64(time.Second)),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orderRepository.FindStuckCharging rows: %w", err)
	}
	return out, nil
}

// FindStuckFailed returns failure-terminal-state orders older than
// minAge, up to limit rows. Backs the saga watchdog subcommand's
// sweep (A5). Status set: {failed (legacy A4), expired (Pattern A
// reservation TTL), payment_failed (Pattern A webhook failure)} —
// all three have the same recovery shape (run compensator → revert
// Redis + mark compensated) and so should all be visible to the
// watchdog.
//
// Same query shape as FindStuckCharging. The partial index from
// migration 000013 (widened from 000011's predicate) covers
// `status IN ('charging', 'pending', 'failed', 'expired',
// 'payment_failed')` so both the reconciler and the watchdog share
// one index. The widening lands with D2 (this PR) so when D5/D6
// add code paths that produce `expired` / `payment_failed` orders,
// the watchdog already covers them — no orphan window where a
// stuck Pattern A failure is invisible to the existing safety net.
//
// "Stuck" means: an order reached one of the failure-terminal
// states but the saga compensator never moved it to Compensated —
// usually because the saga consumer crashed mid-handler, the DLQ
// route swallowed the event, or a Kafka rebalance lost the offset.
// The watchdog detects these and re-drives the existing (idempotent)
// compensator to clear the row.
//
// No FOR UPDATE for the same reason as FindStuckCharging — the
// downstream compensator carries its own row-level lock through the
// UoW transaction; holding a SELECT lock across the compensator
// would block the saga consumer needlessly.
func (r *postgresOrderRepository) FindStuckFailed(
	ctx context.Context, minAge time.Duration, limit int,
) ([]domain.StuckFailed, error) {
	const sqlStmt = `
		SELECT id, EXTRACT(EPOCH FROM (NOW() - updated_at))
		  FROM orders
		 WHERE status IN ('failed', 'expired', 'payment_failed')
		   AND updated_at < NOW() - $1::interval
		 ORDER BY updated_at ASC
		 LIMIT $2`

	intervalLit := fmt.Sprintf("%f seconds", minAge.Seconds())

	rows, err := r.exec.QueryContext(ctx, sqlStmt, intervalLit, limit)
	if err != nil {
		return nil, fmt.Errorf("orderRepository.FindStuckFailed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.StuckFailed
	for rows.Next() {
		var id uuid.UUID
		var ageSeconds float64
		if err := rows.Scan(&id, &ageSeconds); err != nil {
			return nil, fmt.Errorf("orderRepository.FindStuckFailed scan: %w", err)
		}
		out = append(out, domain.StuckFailed{
			ID:  id,
			Age: time.Duration(ageSeconds * float64(time.Second)),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orderRepository.FindStuckFailed rows: %w", err)
	}
	return out, nil
}

// FindExpiredReservations returns awaiting_payment orders whose
// `reserved_until` is at or past `NOW() - gracePeriod`, oldest-first,
// up to `limit` rows. The D6 expiry sweeper's per-tick input.
//
// Why DB-NOW (not app-clock):
//   D5's MarkPaid SQL predicate uses `reserved_until > NOW()` against
//   the same column. If D6 used app-clock for cutoff while D5 uses
//   DB-NOW, app-clock skew on the sweeper pod could pre-empt
//   reservations the DB still considers live (or vice versa). Sharing
//   `NOW()` makes the eligibility boundary unambiguous.
//
// Why the partial index covers this:
//   Migration 000012 added
//   `idx_orders_awaiting_payment_reserved_until ON orders(status,
//    reserved_until) WHERE status='awaiting_payment'`
//   specifically for this query (migration header at lines 55-63
//   names D6 as the consumer). Verify with `EXPLAIN` in the
//   integration test under `SET enable_seqscan=off` if the planner
//   skips it on small CI tables — that's a planner cost-estimate
//   issue, not an index reachability issue.
//
// `Age` is `NOW() - reserved_until` so the sweeper can record the
// `expiry_sweep_age_seconds` SLO histogram + log the per-row age
// at transition time.
func (r *postgresOrderRepository) FindExpiredReservations(
	ctx context.Context, gracePeriod time.Duration, limit int,
) ([]domain.ExpiredReservation, error) {
	const sqlStmt = `
		SELECT id, EXTRACT(EPOCH FROM (NOW() - reserved_until))
		  FROM orders
		 WHERE status = 'awaiting_payment'
		   AND reserved_until <= NOW() - $1::interval
		 ORDER BY reserved_until ASC
		 LIMIT $2`

	intervalLit := fmt.Sprintf("%f seconds", gracePeriod.Seconds())

	rows, err := r.exec.QueryContext(ctx, sqlStmt, intervalLit, limit)
	if err != nil {
		return nil, fmt.Errorf("orderRepository.FindExpiredReservations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.ExpiredReservation
	for rows.Next() {
		var id uuid.UUID
		var ageSeconds float64
		if err := rows.Scan(&id, &ageSeconds); err != nil {
			return nil, fmt.Errorf("orderRepository.FindExpiredReservations scan: %w", err)
		}
		out = append(out, domain.ExpiredReservation{
			ID:  id,
			Age: time.Duration(ageSeconds * float64(time.Second)),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orderRepository.FindExpiredReservations rows: %w", err)
	}
	return out, nil
}

// CountOverdueAfterCutoff returns the count of awaiting_payment rows
// still past `NOW() - gracePeriod` AND the age of the oldest such row.
// Used by the D6 sweeper at the END of each tick (post-resolve) to
// populate the backlog gauges.
//
// Single SQL round-trip via `MIN(reserved_until)` + `COUNT(*)` with
// the same predicate as FindExpiredReservations — same partial index,
// same plan shape. When count is 0, oldestAge is also 0 (zero
// `time.Duration`); `MIN()` of an empty set is NULL but `COALESCE`
// folds it into 0 so the caller doesn't have to special-case.
//
// Failure contract (see `internal/application/expiry/sweeper.go`):
// caller increments `expiry_find_expired_errors_total`, leaves the
// backlog gauges at last-known-good, and returns the error from
// `Sweep`. Already-transitioned rows in the same tick are NOT rolled
// back — count is post-UoW observability.
func (r *postgresOrderRepository) CountOverdueAfterCutoff(
	ctx context.Context, gracePeriod time.Duration,
) (count int, oldestAge time.Duration, err error) {
	const sqlStmt = `
		SELECT COUNT(*),
		       COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(reserved_until))), 0)
		  FROM orders
		 WHERE status = 'awaiting_payment'
		   AND reserved_until <= NOW() - $1::interval`

	intervalLit := fmt.Sprintf("%f seconds", gracePeriod.Seconds())

	var oldestAgeSeconds float64
	if err := r.exec.QueryRowContext(ctx, sqlStmt, intervalLit).Scan(&count, &oldestAgeSeconds); err != nil {
		return 0, 0, fmt.Errorf("orderRepository.CountOverdueAfterCutoff: %w", err)
	}
	return count, time.Duration(oldestAgeSeconds * float64(time.Second)), nil
}

// MarkCharging atomically transitions Pending → Charging. The intent
// log entry — written before the payment service calls the gateway
// so a separate recon process can resolve stuck-mid-flight orders
// via gateway.GetStatus(). See PROJECT_SPEC §A4.
func (r *postgresOrderRepository) MarkCharging(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusCharging, domain.OrderStatusPending)
}

// MarkConfirmed atomically transitions Pending OR Charging → Confirmed.
//
// The Pending → Confirmed edge is TRANSITIONAL during the A4 migration
// window: the new code path goes Pending → Charging → Confirmed, but
// in-flight Pending messages queued before the deploy still resolve
// via the old direct path. A follow-up PR tightens to Charging-only
// after the cutover trigger fires (see §A4).
func (r *postgresOrderRepository) MarkConfirmed(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusConfirmed,
		domain.OrderStatusPending, domain.OrderStatusCharging)
}

// MarkFailed atomically transitions Pending OR Charging → Failed (saga
// path entry). Same transitional-edge rationale as MarkConfirmed.
func (r *postgresOrderRepository) MarkFailed(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusFailed,
		domain.OrderStatusPending, domain.OrderStatusCharging)
}

// MarkCompensated atomically transitions Failed | Expired |
// PaymentFailed → Compensated (saga completion). NOT Pending→
// Compensated — the saga compensator is invoked AFTER one of the
// failure-terminal states has already been written.
//
// The set is wider than the legacy "Failed only" because Pattern A
// (D6 expiry sweeper, D5 webhook failure) produces two new failure-
// terminal states (Expired, PaymentFailed) that ALSO need
// compensation (revert Redis inventory). Same compensator logic
// applies to all three.
func (r *postgresOrderRepository) MarkCompensated(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusCompensated,
		domain.OrderStatusFailed,
		domain.OrderStatusExpired,
		domain.OrderStatusPaymentFailed)
}

// MarkAwaitingPayment atomically transitions Pending → AwaitingPayment.
// The Pattern A entry transition — `BookingService.BookTicket` will
// call this in D3 to create the reservation that the customer pays
// for via POST /orders/:id/pay (D4).
func (r *postgresOrderRepository) MarkAwaitingPayment(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusAwaitingPayment, domain.OrderStatusPending)
}

// MarkPaid atomically transitions AwaitingPayment → Paid (terminal),
// guarded on `reserved_until > NOW()` so a `succeeded` webhook
// arriving after the reservation window elapsed cannot flip the row
// to Paid. The D5 webhook handler routes a 0-rows ErrReservationExpired
// result into a late-success refund path (MarkExpired + emit
// `order.failed` to saga + critical alert; see
// `internal/application/payment/webhook_service.go`). Without this
// clause, a customer paying after their hold expired would land in
// the "user paid for an expired reservation" UX bug — the same race
// SetPaymentIntentID's predicate already documents.
//
// Why a bespoke SQL block instead of the generic `transitionStatus`
// helper: that helper takes only `from ...OrderStatus` predicates;
// MarkPaid additionally needs the wall-clock-comparison predicate.
// Adding a `reservedUntil` parameter to `transitionStatus` for a
// single caller would clutter every other transition with an
// unused argument. The disambiguation tail mirrors `transitionStatus`
// so the contract stays uniform — 0 rows resolves to one of three
// sentinels via a follow-up GetByID:
//
//	ErrOrderNotFound        row truly gone
//	ErrReservationExpired   status awaiting_payment but reserved_until ≤ NOW()
//	ErrInvalidTransition    status not awaiting_payment
//
// The CTE shape (FOR UPDATE-locked source read + UPDATE FROM that
// read + INSERT into history from RETURNING) is the same as
// `transitionStatus` so the audit-log invariant holds: status change
// and history insert always succeed or fail atomically together.
func (r *postgresOrderRepository) MarkPaid(ctx context.Context, id uuid.UUID) error {
	const sqlStmt = `
		WITH before AS (
			SELECT id, status FROM orders WHERE id = $1 FOR UPDATE
		),
		transitioned AS (
			UPDATE orders
			   SET status = 'paid', updated_at = NOW()
			  FROM before
			 WHERE orders.id = before.id
			   AND before.status = 'awaiting_payment'
			   AND orders.reserved_until > NOW()
			RETURNING orders.id, before.status AS from_status
		)
		INSERT INTO order_status_history (order_id, from_status, to_status)
		SELECT id, from_status, 'paid' FROM transitioned`

	res, err := r.exec.ExecContext(ctx, sqlStmt, id)
	if err != nil {
		return fmt.Errorf("orderRepository.MarkPaid id=%s: %w", id, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("orderRepository.MarkPaid rows id=%s: %w", id, err)
	}
	if rowsAffected == 1 {
		return nil
	}
	return r.classifyPaidGuardFailure(ctx, id, "")
}

// classifyPaidGuardFailure runs the GetByID disambiguation shared by
// MarkPaid and SetPaymentIntentID. Both repository calls guard on
// `status = 'awaiting_payment' AND reserved_until > NOW()` and need to
// distinguish "row gone" / "reservation expired" / "status no longer
// awaiting_payment" so the D5 webhook handler can route precisely.
//
// `expectedIntentID` is the orderID-keyed intent we wanted to write
// (only set by SetPaymentIntentID; MarkPaid passes ""). When non-empty
// AND the row is still in awaiting_payment with live reserved_until,
// the only remaining reason a 0-rows could have happened is an intent
// id mismatch with what's already on the row — that resolves to
// ErrInvalidTransition (intent uniqueness is part of the same logical
// transition guard).
func (r *postgresOrderRepository) classifyPaidGuardFailure(ctx context.Context, id uuid.UUID, expectedIntentID string) error {
	cur, getErr := r.GetByID(ctx, id)
	if errors.Is(getErr, domain.ErrOrderNotFound) {
		return domain.ErrOrderNotFound
	}
	if getErr != nil {
		return fmt.Errorf("orderRepository.classifyPaidGuardFailure id=%s: %w", id, getErr)
	}
	if cur.Status() != domain.OrderStatusAwaitingPayment {
		return fmt.Errorf("orderRepository id=%s status=%s: %w", id, cur.Status(), domain.ErrInvalidTransition)
	}
	// status is awaiting_payment. Two remaining reasons the predicate
	// failed:
	//   1. reserved_until ≤ NOW() (always possible)
	//   2. intent id mismatch (only if expectedIntentID was passed)
	// Order matters: check reservation FIRST so the late-success
	// refund path is reachable even when the row also has a different
	// intent id (rare but possible — gateway returned an intent we
	// couldn't persist, then the caller retried with a fresh intent).
	if !cur.ReservedUntil().After(time.Now()) {
		return fmt.Errorf("orderRepository id=%s: %w", id, domain.ErrReservationExpired)
	}
	if expectedIntentID != "" && cur.PaymentIntentID() != "" && cur.PaymentIntentID() != expectedIntentID {
		return fmt.Errorf("orderRepository id=%s: intent id mismatch (have=%q want=%q): %w",
			id, cur.PaymentIntentID(), expectedIntentID, domain.ErrInvalidTransition)
	}
	// Got here = the row legitimately matches the predicate but the
	// UPDATE saw 0 rows. Most likely a transient race between the
	// UPDATE's snapshot and the disambiguating GetByID's snapshot
	// (the row flipped Paid/Expired and back, somehow). Surface as a
	// non-sentinel error so it's loud rather than silent.
	return fmt.Errorf("orderRepository id=%s: 0 rows under apparently-valid predicate (status=%s reserved_until=%s)",
		id, cur.Status(), cur.ReservedUntil().Format(time.RFC3339))
}

// MarkExpired atomically transitions AwaitingPayment → Expired.
// Triggered by the reservation expiry sweeper (D6) when
// `reserved_until < NOW()` and the order has been in AwaitingPayment
// past its TTL without a successful payment webhook.
//
// Expired is NOT terminal — the saga compensator runs after Expired
// (Expired → Compensated) to revert Redis inventory.
func (r *postgresOrderRepository) MarkExpired(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusExpired, domain.OrderStatusAwaitingPayment)
}

// MarkPaymentFailed atomically transitions AwaitingPayment →
// PaymentFailed. Triggered by the POST /webhook/payment failure
// callback (D5).
//
// PaymentFailed is NOT terminal — the saga compensator runs after
// PaymentFailed (PaymentFailed → Compensated) to revert Redis
// inventory.
func (r *postgresOrderRepository) MarkPaymentFailed(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusPaymentFailed, domain.OrderStatusAwaitingPayment)
}

// SetPaymentIntentID persists the gateway-assigned PaymentIntent id
// onto an order. Race-safe: the SQL UPDATE includes
//
//	WHERE status = 'awaiting_payment'
//	  AND reserved_until > NOW()
//	  AND (payment_intent_id IS NULL OR payment_intent_id = $2)
//
// so:
//   - Concurrent /pay calls with the SAME intent_id (gateway is
//     idempotent on orderID per PaymentIntentCreator's contract)
//     end up with the same row state. The second UPDATE is a
//     no-op-equivalent (`SET col = same_value`), still 1 row affected.
//   - A /pay call against an order that's been flipped (Paid /
//     Expired / PaymentFailed by webhook or sweeper) safely 0-rows-
//     affected → ErrOrderNotFound. The webhook race is the live
//     concern here: webhook flips status to Paid milliseconds before
//     our /pay UPDATE lands; the predicate rejects the late /pay
//     write so the row's status doesn't get rolled back accidentally
//     (status stays Paid; the /pay client gets an error and can
//     re-fetch via GET /orders/:id to see the actual state).
//   - A reservation that elapsed during the gateway round-trip
//     (service.go validates reserved_until at read time, then
//     ~50-200ms of gateway latency, then this UPDATE) is rejected
//     by `reserved_until > NOW()`. Without this clause, an order
//     could land in "AwaitingPayment with payment_intent_id but
//     reserved_until already past" — D6's sweeper would expire it,
//     but the client meanwhile might call confirm-payment via
//     Stripe Elements and the D5 webhook would mark it Paid,
//     creating a "user paid for an expired reservation" UX bug.
//     Defensive belt-and-suspenders to the service-level check.
//   - A different intent_id arriving (which should be impossible
//     under PaymentIntentCreator's idempotency contract — same
//     orderID always returns the same intent — but defensively
//     handled) is rejected by the third clause.
func (r *postgresOrderRepository) SetPaymentIntentID(ctx context.Context, id uuid.UUID, paymentIntentID string) error {
	res, err := r.exec.ExecContext(ctx, `
		UPDATE orders
		   SET payment_intent_id = $2
		 WHERE id = $1
		   AND status = 'awaiting_payment'
		   AND reserved_until > NOW()
		   AND (payment_intent_id IS NULL OR payment_intent_id = $2)
	`, id, paymentIntentID)
	if err != nil {
		return fmt.Errorf("orderRepository.SetPaymentIntentID id=%s: %w", id, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("orderRepository.SetPaymentIntentID rows id=%s: %w", id, err)
	}
	if rowsAffected == 0 {
		// 0 rows: disambiguate via GetByID into one of three sentinels
		// (matches MarkPaid's contract). The D5 webhook handler relies
		// on this precise classification to route the orphan-repair
		// branch into the late-success / re-read / propagate paths
		// instead of bubbling all of them out as a generic 500. D4 /pay
		// also benefits — `ErrReservationExpired` lets the handler
		// return a clear "reservation expired" message instead of a
		// generic 404.
		return r.classifyPaidGuardFailure(ctx, id, paymentIntentID)
	}
	return nil
}

// FindByPaymentIntentID is the D5 webhook fallback path — looks up an
// order by its `payment_intent_id`. Used when an inbound
// `payment_intent.*` envelope's `metadata.order_id` is absent or
// malformed; metadata.order_id remains the primary lookup so this
// path only fires for legacy intents (created before the metadata
// field landed) or in rescue scenarios.
//
// Backed by `orders_payment_intent_id_unique_idx` (migration 000015 —
// partial unique on `payment_intent_id WHERE payment_intent_id IS NOT
// NULL`). The UNIQUE constraint enforces the PaymentIntentCreator
// idempotency contract at the persistence layer (each gateway-issued
// intent maps to at most one order); without it, a future repository
// bug could write two orders with the same intent_id and this method
// would silently return whichever postgres picked.
func (r *postgresOrderRepository) FindByPaymentIntentID(ctx context.Context, paymentIntentID string) (domain.Order, error) {
	if paymentIntentID == "" {
		// Defensive: a "" lookup would match the partial-index
		// exclusion clause and return zero rows, but the call site
		// only reaches here when the webhook handler couldn't read
		// metadata.order_id either — surfacing the empty input loudly
		// is better than silently NotFound-ing.
		return domain.Order{}, fmt.Errorf("orderRepository.FindByPaymentIntentID: empty payment_intent_id")
	}
	var row orderRow
	err := row.scanInto(r.exec.QueryRowContext(ctx,
		"SELECT "+orderColumns+" FROM orders WHERE payment_intent_id = $1", paymentIntentID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Order{}, domain.ErrOrderNotFound
		}
		return domain.Order{}, fmt.Errorf("orderRepository.FindByPaymentIntentID intent=%s: %w", paymentIntentID, err)
	}
	return row.toDomain(), nil
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

// --- TicketTypeRepository (D4.1) ---

type postgresTicketTypeRepository struct {
	exec dbExecutor
}

// NewPostgresTicketTypeRepository returns the long-lived non-tx repo.
// Mirrors NewPostgresEventRepository; the fx provider in module.go
// re-exposes this under `domain.TicketTypeRepository` for application-
// layer consumers, and PostgresUnitOfWork can call WithTx on it.
func NewPostgresTicketTypeRepository(db *sql.DB) *postgresTicketTypeRepository {
	return &postgresTicketTypeRepository{exec: db}
}

func (r *postgresTicketTypeRepository) WithTx(tx *sql.Tx) *postgresTicketTypeRepository {
	return &postgresTicketTypeRepository{exec: tx}
}

func (r *postgresTicketTypeRepository) Create(ctx context.Context, t domain.TicketType) (domain.TicketType, error) {
	row := ticketTypeRowFromDomain(t)
	if _, err := r.exec.ExecContext(ctx,
		"INSERT INTO event_ticket_types ("+ticketTypeColumns+") VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)",
		row.ID, row.EventID, row.Name, row.PriceCents, row.Currency,
		row.TotalTickets, row.AvailableTickets,
		row.SaleStartsAt, row.SaleEndsAt,
		row.PerUserLimit, row.AreaLabel,
		row.Version,
	); err != nil {
		// 23505 = unique_violation. The only unique constraint on
		// event_ticket_types is `uq_ticket_type_name_per_event`
		// (event_id, name), so a 23505 here means an admin tried to
		// create two ticket types with the same name for the same
		// event. Map to the typed sentinel so the API boundary can
		// branch with `errors.Is(err, domain.ErrTicketTypeNameTaken)`
		// without string-matching the postgres error.
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.TicketType{}, domain.ErrTicketTypeNameTaken
		}
		return domain.TicketType{}, fmt.Errorf("ticketTypeRepository.Create: %w", err)
	}
	return row.toDomain(), nil
}

func (r *postgresTicketTypeRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.TicketType, error) {
	var row ticketTypeRow
	if err := row.scanInto(r.exec.QueryRowContext(ctx, "SELECT "+ticketTypeColumns+" FROM event_ticket_types WHERE id = $1", id)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TicketType{}, domain.ErrTicketTypeNotFound
		}
		return domain.TicketType{}, fmt.Errorf("ticketTypeRepository.GetByID id=%s: %w", id, err)
	}
	return row.toDomain(), nil
}

// Delete removes a ticket type by id. Idempotent (DELETE on a missing
// row returns rowsAffected=0, which we don't surface as an error —
// the compensation path may run after an earlier partial delete).
// See domain.TicketTypeRepository.Delete doc for the rationale.
//
// Forensics note: rowsAffected is intentionally NOT read or logged
// here. The repository layer is observability-unaware by design (the
// tracing decorator adds spans; metrics layer adds db_pool / db
// stats); adding a debug log per delete from inside the repo would
// fight that convention and produce a per-ticket-type-delete debug
// line in production. Operators investigating compensation behaviour
// can correlate via the higher-level event_service Warn/Error logs
// (which DO carry tag.TicketTypeID + tag.EventID) plus the postgres-
// side query metrics.
func (r *postgresTicketTypeRepository) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := r.exec.ExecContext(ctx, "DELETE FROM event_ticket_types WHERE id = $1", id); err != nil {
		return fmt.Errorf("ticketTypeRepository.Delete id=%s: %w", id, err)
	}
	return nil
}

// ListByEventID returns all ticket types belonging to an event,
// ordered by id (UUIDv7 → time-prefixed → insertion order). Empty
// result is a nil slice, not an error — events with no ticket types
// are valid pre-D4.1 rows during the migration window.
//
// Semantic caveat: this query does NOT verify event existence. A
// non-existent eventID returns `(nil, nil)` — operationally
// indistinguishable from "event exists but has no ticket types yet."
// API endpoints that need 404-on-missing-event must call
// EventRepository.GetByID first (see future GET /api/v1/events/:id
// handler). Once D4.1 migration window closes and every event always
// has ≥ 1 ticket type, this assumption can tighten to "empty list
// implies event-not-found" — but only after the legacy-row floor
// hits zero in production.
func (r *postgresTicketTypeRepository) ListByEventID(ctx context.Context, eventID uuid.UUID) ([]domain.TicketType, error) {
	rows, err := r.exec.QueryContext(ctx,
		"SELECT "+ticketTypeColumns+" FROM event_ticket_types WHERE event_id = $1 ORDER BY id ASC", eventID)
	if err != nil {
		return nil, fmt.Errorf("ticketTypeRepository.ListByEventID query (event=%s): %w", eventID, err)
	}
	defer func() { _ = rows.Close() }() // close error is non-actionable; iter err already checked below

	var out []domain.TicketType
	for rows.Next() {
		var row ticketTypeRow
		if err := row.scanInto(rows); err != nil {
			return nil, fmt.Errorf("ticketTypeRepository.ListByEventID scan: %w", err)
		}
		out = append(out, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ticketTypeRepository.ListByEventID iter: %w", err)
	}
	return out, nil
}

// DecrementTicket atomically deducts inventory on the event_ticket_types
// row. The `available_tickets >= $2` predicate makes the decrement
// atomic under concurrent writers — a row affected of 0 means either
// (a) the row was over-drawn (the dominant case) or (b) the id has no
// row. We conflate both into ErrTicketTypeSoldOut to match the Event
// repository's pattern (case (b) is a data-corruption rarity that
// shouldn't appear in normal flows; a follow-up GetByID would distinguish
// it but at the cost of an extra round-trip on the hot path).
//
// D4.1: this is the new SoT for inventory decrements. The legacy
// EventRepository.DecrementTicket is preserved for backward compat
// (events.available_tickets is frozen — no longer written by the
// worker as of D4.1 follow-up) and will be removed in a follow-up
// migration.
func (r *postgresTicketTypeRepository) DecrementTicket(ctx context.Context, id uuid.UUID, quantity int) error {
	res, err := r.exec.ExecContext(ctx,
		"UPDATE event_ticket_types SET available_tickets = available_tickets - $2 WHERE id = $1 AND available_tickets >= $2",
		id, quantity)
	if err != nil {
		return fmt.Errorf("ticketTypeRepository.DecrementTicket exec (ticket_type=%s, qty=%d): %w", id, quantity, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("ticketTypeRepository.DecrementTicket rows: %w", err)
	}
	if rowsAffected == 0 {
		return domain.ErrTicketTypeSoldOut
	}
	return nil
}

// SumAvailableByEventID is the per-event aggregate that replaces the
// frozen `events.available_tickets` reader paths post-D4.1 follow-up
// (rehydrate + inventory drift detection). The query is COALESCE'd to
// 0 because SUM over zero rows would return NULL — which would scan
// into Go's int as 0 anyway, but COALESCE makes the intent explicit
// and protects against driver-version differences on NULL→int scans.
func (r *postgresTicketTypeRepository) SumAvailableByEventID(ctx context.Context, eventID uuid.UUID) (int, error) {
	var sum int
	if err := r.exec.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(available_tickets), 0) FROM event_ticket_types WHERE event_id = $1",
		eventID,
	).Scan(&sum); err != nil {
		return 0, fmt.Errorf("ticketTypeRepository.SumAvailableByEventID event_id=%s: %w", eventID, err)
	}
	return sum, nil
}

// IncrementTicket restores inventory. Used by saga compensation. The
// `+ $2 <= total_tickets` predicate prevents over-restore — symmetric
// with EventRepository.IncrementTicket. The schema's
// `check_ticket_type_available_tickets_non_negative` CHECK constraint
// is a defense-in-depth backup; the SQL guard surfaces over-restore as
// rowsAffected==0 (a wrapped error caller can act on) instead of a raw
// constraint-violation panic.
func (r *postgresTicketTypeRepository) IncrementTicket(ctx context.Context, id uuid.UUID, quantity int) error {
	res, err := r.exec.ExecContext(ctx,
		"UPDATE event_ticket_types SET available_tickets = available_tickets + $2 WHERE id = $1 AND available_tickets + $2 <= total_tickets",
		id, quantity)
	if err != nil {
		return fmt.Errorf("ticketTypeRepository.IncrementTicket exec (ticket_type=%s, qty=%d): %w", id, quantity, err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("ticketTypeRepository.IncrementTicket rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("ticketTypeRepository.IncrementTicket: ticket_type %s not found or exceeds total tickets", id)
	}
	return nil
}
