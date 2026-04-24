package postgres

import (
	"booking_monitor/internal/application"
	"booking_monitor/internal/domain"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type txKey struct{}

// PostgresUnitOfWork implements domain.UnitOfWork
type PostgresUnitOfWork struct {
	db      *sql.DB
	logger  *mlog.Logger
	metrics application.DBMetrics
}

func NewPostgresUnitOfWork(db *sql.DB, logger *mlog.Logger, metrics application.DBMetrics) domain.UnitOfWork {
	return &PostgresUnitOfWork{
		db:      db,
		logger:  logger.With(mlog.String("component", "unit_of_work")),
		metrics: metrics,
	}
}

func (u *PostgresUnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	// If already in transaction, just execute
	if _, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return fn(ctx)
	}

	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// Inject tx into context
	txCtx := context.WithValue(ctx, txKey{}, tx)

	if err := fn(txCtx); err != nil {
		// Rollback can legitimately return sql.ErrTxDone when the driver
		// has already closed the tx after a fatal error (broken conn,
		// deadlock, etc.) — that's expected, not a failure. Any OTHER
		// rollback error is a real problem: the tx may be left hanging,
		// connection may be poisoned, or partial state may leak. Log it
		// but do NOT overwrite the fn error — callers need the original
		// cause, not the rollback failure.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			u.metrics.RecordRollbackFailure()
			u.logger.Error(ctx, "tx rollback failed after fn error",
				tag.Error(rbErr),
				mlog.NamedError("fn_error", err),
			)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	return nil
}

// DBExecutor Interface for common methods between *sql.DB and *sql.Tx
type DBExecutor interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// getExecutor extracts tx from context or returns db
func (r *postgresEventRepository) getExecutor(ctx context.Context) DBExecutor {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return r.db
}

func (r *postgresOrderRepository) getExecutor(ctx context.Context) DBExecutor {
	if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
		return tx
	}
	return r.db
}
