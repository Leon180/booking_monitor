package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"booking_monitor/internal/infrastructure/config"
	"booking_monitor/internal/infrastructure/observability"
	mlog "booking_monitor/internal/log"
	"booking_monitor/internal/log/tag"
)

// provideDB opens Postgres, applies pool settings, then retries Ping
// until the DB is reachable or the budget expires. PingContext is used
// so a stuck network call cannot exceed our per-attempt timeout.
//
// Lives in bootstrap (not infrastructure/persistence/postgres) because
// the retry budget + pool tuning are deployment-time concerns belonging
// at process startup, not inside the repository layer that consumes
// the resulting *sql.DB.
func provideDB(cfg *config.Config, logger *mlog.Logger) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.Postgres.DSN)
	if err != nil {
		return nil, fmt.Errorf("provideDB: sql.Open: %w", err)
	}

	// Pool settings BEFORE the retry loop so retries exercise the
	// configured limits (previously setters ran after the first Ping,
	// which meant the initial probe burned a slot at default pool size).
	db.SetMaxOpenConns(cfg.Postgres.MaxOpenConns)
	db.SetMaxIdleConns(cfg.Postgres.MaxIdleConns)
	db.SetConnMaxIdleTime(cfg.Postgres.MaxIdleTime)
	// MaxLifetime forces periodic conn recycling; bounds long-lived
	// connection staleness (prepared-stmt caches, PgBouncer auth drift).
	db.SetConnMaxLifetime(cfg.Postgres.MaxLifetime)

	attempts := cfg.Postgres.PingAttempts
	interval := cfg.Postgres.PingInterval
	perAttempt := cfg.Postgres.PingPerAttempt

	totalBudget := time.Duration(attempts) * (interval + perAttempt)
	ctx, cancel := context.WithTimeout(context.Background(), totalBudget)
	defer cancel()

	var pingErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, perAttempt)
		pingErr = db.PingContext(attemptCtx)
		attemptCancel()
		if pingErr == nil {
			return db, nil
		}
		logger.L().Warn("waiting for Postgres", mlog.Int("attempt", attempt), tag.Error(pingErr))
		if attempt == attempts {
			break
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return nil, fmt.Errorf("provideDB: context cancelled: %w", ctx.Err())
		}
	}
	return nil, fmt.Errorf("provideDB: postgres unreachable after %d attempts: %w", attempts, pingErr)
}

// registerDBPoolCollector publishes the *sql.DB pool stats as
// Prometheus gauges/counters (see observability.DBPoolCollector). Wired
// in fx.Invoke so the `*sql.DB` exists by the time we register; failure
// is fatal because pool saturation is the most common production-failure
// mode and we'd rather surface a real registration bug at boot than
// silently lose the gauges.
//
// AlreadyRegisteredError is treated as success — re-invocations
// (test re-import, fx restart) leave the prior collector in place,
// which is the desired state, not a failure mode.
func registerDBPoolCollector(db *sql.DB) error {
	if err := prometheus.DefaultRegisterer.Register(observability.NewDBPoolCollector(db)); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return fmt.Errorf("registerDBPoolCollector: %w", err)
		}
	}
	return nil
}

// registerOutboxPendingCollector publishes the count of unprocessed
// transactional-outbox rows as a gauge (see
// observability.OutboxPendingCollector). Idempotent
// AlreadyRegisteredError handling like registerDBPoolCollector, with
// one addition: log at Warn level when re-registration occurs.
//
// Why the extra log: this collector is the sole source of the
// `outbox_pending_count` gauge that drives a critical alert path. If
// a future code path constructs a second OutboxPendingCollector
// against a DIFFERENT *sql.DB (e.g., a misconfigured fx wiring with
// duplicate provides), the AlreadyRegisteredError swallow would
// silently keep the FIRST DB's collector alive — the second DB's
// outbox state would be invisible in metrics, and dashboards would
// silently mis-report. The Warn line makes the reuse auditable so
// the mismatch surfaces in logs even when registration "succeeds".
func registerOutboxPendingCollector(db *sql.DB, logger *mlog.Logger) error {
	if err := prometheus.DefaultRegisterer.Register(observability.NewOutboxPendingCollector(db)); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return fmt.Errorf("registerOutboxPendingCollector: %w", err)
		}
		logger.Warn(context.Background(),
			"outbox pending collector already registered, reusing existing instance",
			mlog.String("existing_type", fmt.Sprintf("%T", are.ExistingCollector)))
	}
	return nil
}
