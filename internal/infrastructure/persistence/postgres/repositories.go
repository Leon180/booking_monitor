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

// MarkPaid atomically transitions AwaitingPayment → Paid (terminal).
// Triggered by the POST /webhook/payment success callback (D5).
//
// Strictly AwaitingPayment-only as source; not Pending → Paid. The
// webhook only fires after a payment intent was created, which only
// happens after the order is in AwaitingPayment.
func (r *postgresOrderRepository) MarkPaid(ctx context.Context, id uuid.UUID) error {
	return r.transitionStatus(ctx, id, domain.OrderStatusPaid, domain.OrderStatusAwaitingPayment)
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
		// 0 rows = order doesn't exist, status isn't awaiting_payment,
		// or already has a different intent_id. ErrOrderNotFound is
		// the closest existing sentinel; the application service can
		// re-read the order to disambiguate if needed.
		return domain.ErrOrderNotFound
	}
	return nil
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
