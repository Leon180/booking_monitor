package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"booking_monitor/internal/application"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// PostgresUnitOfWork implements application.UnitOfWork on top of
// `database/sql`. The UoW holds the long-lived non-tx repos (each
// produced by `NewPostgresXRepository`) and clones them with `WithTx`
// inside `Do`, so every method call inside the closure runs against
// the same `*sql.Tx`.
//
// Why postgres-package, not application-package: the implementation is
// inherently database-driver-coupled (BeginTx, Rollback, ErrTxDone).
// The application port lives one layer in (internal/application/uow.go);
// this struct satisfies that interface — DIP, exactly as Clean
// Architecture prescribes.
type PostgresUnitOfWork struct {
	db             *sql.DB
	orderRepo      *postgresOrderRepository
	eventRepo      *postgresEventRepository
	outboxRepo     *postgresOutboxRepository
	ticketTypeRepo *postgresTicketTypeRepository
	logger         *mlog.Logger
	metrics        application.DBMetrics
}

func NewPostgresUnitOfWork(
	db *sql.DB,
	orderRepo *postgresOrderRepository,
	eventRepo *postgresEventRepository,
	outboxRepo *postgresOutboxRepository,
	ticketTypeRepo *postgresTicketTypeRepository,
	logger *mlog.Logger,
	metrics application.DBMetrics,
) application.UnitOfWork {
	return &PostgresUnitOfWork{
		db:             db,
		orderRepo:      orderRepo,
		eventRepo:      eventRepo,
		outboxRepo:     outboxRepo,
		ticketTypeRepo: ticketTypeRepo,
		logger:         logger.With(mlog.String("component", "unit_of_work")),
		metrics:        metrics,
	}
}

// Do begins a transaction, builds a fresh Repositories bundle bound to
// that tx, and hands it to fn. Commit on nil return; rollback on error.
//
// The Repositories bundle is constructed PER INVOCATION and never
// cached on the UoW struct — caching would let a tx-bound repo leak to
// a concurrent caller (Morrison atomic-repositories foot-gun). Each
// `WithTx` returns a fresh struct, so two concurrent `Do` calls cannot
// see each other's tx.
//
// The previous PR-34 era implementation used `context.WithValue(txKey)`
// to thread the tx through repo calls and silently fell back to the
// non-tx pool when the contract was violated. PR 35 removes the ctx
// machinery entirely — `repos` is a concrete parameter, so "passing
// the wrong ctx" is no longer a possible state.
func (u *PostgresUnitOfWork) Do(ctx context.Context, fn func(repos *application.Repositories) error) error {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	repos := &application.Repositories{
		Order:      u.orderRepo.WithTx(tx),
		Event:      u.eventRepo.WithTx(tx),
		Outbox:     u.outboxRepo.WithTx(tx),
		TicketType: u.ticketTypeRepo.WithTx(tx),
	}

	if err := fn(repos); err != nil {
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
