package postgres

import (
	"context"
	"database/sql"
	"sync"

	"booking_monitor/internal/domain"
)

type pgAdvisoryLock struct {
	db   *sql.DB
	conn *sql.Conn
	mu   sync.Mutex
}

// NewPostgresDistributedLock creates a DistributedLock backed by Postgres
// session-level advisory locks. It internally manages a dedicated *sql.Conn
// to ensure the lock is held across connection pools safely.
func NewPostgresDistributedLock(db *sql.DB) domain.DistributedLock {
	return &pgAdvisoryLock{db: db}
}

func (l *pgAdvisoryLock) TryLock(ctx context.Context, lockID int64) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// If we already hold the connection (and thereby the session lock),
	// we are currently the leader. No need to query the DB again.
	if l.conn != nil {
		return true, nil
	}

	conn, err := l.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	l.conn = conn

	var acquired bool
	err = l.conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired)
	if err != nil || !acquired {
		// If we failed to acquire or got an error, release the connection
		// so we don't exhaust the connection pool while in Standby mode.
		if l.conn != nil {
			_ = l.conn.Close()
			l.conn = nil
		}
		return false, err
	}

	return true, nil
}

func (l *pgAdvisoryLock) Unlock(ctx context.Context, lockID int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.conn == nil {
		return nil // Not locked or already released
	}

	defer func() {
		_ = l.conn.Close()
		l.conn = nil
	}()

	var unlocked bool
	err := l.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", lockID).Scan(&unlocked)
	return err
}
