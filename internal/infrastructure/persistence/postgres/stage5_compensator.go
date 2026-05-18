package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"booking_monitor/internal/application/expiry"
	"booking_monitor/internal/domain"
)

type stage5Compensator struct {
	db            *sql.DB
	inventoryRepo domain.InventoryRepository
}

var _ expiry.Compensator = (*stage5Compensator)(nil)

// NewStage5Compensator returns the Stage 5 abandon-path Compensator.
// It is the symmetric inverse of the worker's UoW forward path:
//
//   - forward (worker): Redis Lua deduct → INSERT order → DECREMENT event_ticket_types
//   - backward (this):  RevertInventory → UPDATE event_ticket_types += qty → SET status='compensated'
//
// Operation ordering: RevertInventory (Redis) fires BEFORE the PG UPDATEs within the
// same Compensate call. Redis is NOT inside the sql.Tx — the two stores have no shared
// atomicity boundary. The failure windows are:
//
//   - RevertInventory succeeds, PG UPDATE/Commit fails: Redis inventory is incremented
//     and the SETNX idempotency key is set. On the next sweeper retry, revert.lua sees
//     the key and skips the INCRBY (correct), then the PG UPDATEs succeed. Self-healing.
//
//   - RevertInventory fails: the whole call returns an error. Neither Redis nor PG was
//     changed. Sweeper retries cleanly next tick.
//
// This ordering matches Stages 2-3 (settled in PR #106). The alternative
// (PG-commit-first, Redis-after) would require a separate compensating revert.lua
// call on PG failure, which is harder to reason about under retry.
func NewStage5Compensator(db *sql.DB, inventoryRepo domain.InventoryRepository) expiry.Compensator {
	return &stage5Compensator{db: db, inventoryRepo: inventoryRepo}
}

func (s *stage5Compensator) Compensate(ctx context.Context, orderID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// ticket_type_id is UUID NULL (migration 000012 added it nullable;
	// 000014 renamed). Scanning into uuid.UUID fails with a non-ErrNoRows
	// error on NULL rows, masking the sweeper-vs-worker race. Use
	// uuid.NullUUID + .Valid guard to handle both NULL and missing rows.
	var (
		ttID   uuid.NullUUID
		qty    int
		status string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT ticket_type_id, quantity, status
		   FROM orders
		  WHERE id = $1
		    FOR UPDATE`,
		orderID).Scan(&ttID, &qty, &status)
	if errors.Is(err, sql.ErrNoRows) {
		// Sweeper selected this row on a previous tick; by lock time the
		// row was cleared by a concurrent confirm or worker PEL retry.
		return expiry.ErrCompensateNotEligible
	}
	if err != nil {
		return fmt.Errorf("lock order: %w", err)
	}
	if status != string(domain.OrderStatusAwaitingPayment) {
		return expiry.ErrCompensateNotEligible
	}
	if !ttID.Valid {
		// Legacy pre-D4.1 row: ticket_type_id is NULL. Redis inventory is
		// keyed by ticket_type_id so we cannot route the revert. Skip —
		// these rows require manual operator review (data migration backlog).
		return expiry.ErrCompensateNotEligible
	}

	// Redis revert FIRST (inside the tx). On tx rollback the SQL is
	// undone but the Redis INCRBY has already fired. The SETNX guard
	// (saga:reverted:order:<id>) prevents a double-INCRBY on the next
	// retry, keeping Redis and PG consistent under retry.
	if err = s.inventoryRepo.RevertInventory(ctx, ttID.UUID, qty, "order:"+orderID.String()); err != nil {
		return fmt.Errorf("redis revert: %w", err)
	}

	// Symmetric PG increment — the worker UoW decremented available_tickets
	// on the forward path, so compensation MUST increment it back. Zero
	// RowsAffected means an orphaned order (no matching event_ticket_types
	// row); hard-fail to surface the data corruption rather than silently
	// leaking inventory.
	res, err := tx.ExecContext(ctx,
		`UPDATE event_ticket_types
		    SET available_tickets = available_tickets + $1,
		        version = version + 1
		  WHERE id = $2`,
		qty, ttID.UUID)
	if err != nil {
		return fmt.Errorf("revert pg inventory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revert pg inventory rows-affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("revert pg inventory: no event_ticket_types row for id=%s — orphaned order, manual review needed", ttID.UUID)
	}

	// Raw SQL status transition — NOT via repos.Order.MarkCompensated.
	// The domain state machine rejects awaiting_payment → compensated; the
	// abandon path intentionally crosses that boundary directly (same pattern
	// as Stage 1-3). The SELECT FOR UPDATE above holds the row lock through
	// this UPDATE, so the WHERE guard should always match. If RowsAffected==0
	// something unexpected changed the row despite the lock; hard-fail rather
	// than silently committing a state where Redis was reverted but PG was not.
	res2, err := tx.ExecContext(ctx,
		`UPDATE orders
		    SET status = 'compensated'
		  WHERE id = $1
		    AND status = 'awaiting_payment'`,
		orderID)
	if err != nil {
		return fmt.Errorf("mark compensated order=%s: %w", orderID, err)
	}
	if n2, err := res2.RowsAffected(); err != nil {
		return fmt.Errorf("mark compensated rows-affected order=%s: %w", orderID, err)
	} else if n2 == 0 {
		return fmt.Errorf("mark compensated order=%s: row not in awaiting_payment despite FOR UPDATE lock — unexpected concurrent state change", orderID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("compensate commit order=%s: %w", orderID, err)
	}
	return nil
}
