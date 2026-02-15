package postgres

import (
	"booking_monitor/internal/domain"
	"context"
	"database/sql"
	"fmt"
)

type txKey struct{}

// PostgresUnitOfWork implements domain.UnitOfWork
type PostgresUnitOfWork struct {
	db *sql.DB
}

func NewPostgresUnitOfWork(db *sql.DB) domain.UnitOfWork {
	return &PostgresUnitOfWork{db: db}
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
		_ = tx.Rollback()
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
