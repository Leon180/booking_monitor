# Review: Persistence & Transactions

**Branch:** `review/persistence` **Agent:** `go-reviewer` **Date:** 2026-04-11
**Scope:** `internal/infrastructure/persistence/postgres/**`, `internal/domain/uow.go`, `deploy/postgres/migrations/**`

## Summary

All SQL queries use parameterized placeholders (`$1`, `$2`, …) — no injection vectors found. The transactional outbox write is correctly atomic (order + outbox event in the same `UnitOfWork.Do` call). The advisory-lock recursion bug (commit f56ab82) is confirmed fixed. Five findings remain across correctness, robustness, and maintainability.

---

## Findings (severity-ranked)

### [HIGH] `FOR UPDATE` on `GetByID` deadlocks when called outside a transaction

- **File:** [internal/infrastructure/persistence/postgres/repositories.go:33](../../internal/infrastructure/persistence/postgres/repositories.go#L33)
- **Issue:** `GetByID` always appends `FOR UPDATE` to the `SELECT` on `events`. When called from a non-transactional context (e.g. the `GET /api/v1/events/:id` handler, or any read path where no `sql.Tx` is in the context) the lock is acquired on an auto-commit connection and released immediately — but the query still executes with row-level locking intent, adding unnecessary lock overhead. More critically, if `GetByID` is ever called concurrently inside different transactions on the same event row (e.g. two saga compensator paths), they will serialize or deadlock rather than reading stale data harmlessly.
- **Impact:** Unnecessary lock contention on the hot read path; potential deadlock between concurrent saga compensations targeting the same event.
- **Fix:** Remove `FOR UPDATE` from `GetByID`. Add a separate `GetByIDForUpdate(ctx, id)` method (or accept a `forUpdate bool` parameter) and call that only inside the transactional booking/worker path. The worker already uses `DecrementTicket` atomically, so `GetByID` in that flow is only a read.

---

### [HIGH] `UpdateStatus` errors are unwrapped — callers lose repository context

- **File:** [internal/infrastructure/persistence/postgres/repositories.go:161](../../internal/infrastructure/persistence/postgres/repositories.go#L161)
- **Issue:** Every `return err` in the repository layer (lines 57, 65, 70, 96, 161, 169, 174, 190, 195, 248) returns the raw `database/sql` / `pq` error with no `fmt.Errorf("...: %w", err)` wrapping. The project's own coding style (`.claude/rules/golang/coding-style.md`) mandates error wrapping with context at every level.
- **Impact:** Log lines and error chains show cryptic driver errors like `pq: duplicate key value violates…` with no indication of which repository method or which entity ID triggered it. Debugging latent production issues becomes significantly harder.
- **Fix:** Wrap at every `return err` site, for example:
  ```go
  return fmt.Errorf("eventRepo.Update event=%d: %w", event.ID, err)
  ```
  The `Create` method on `postgresOrderRepository` already demonstrates the correct pattern by checking `pgErr.Code` — extend that discipline to all other paths.

---

### [HIGH] `sql.ErrNoRows` compared with `==` instead of `errors.Is`

- **File:** [internal/infrastructure/persistence/postgres/repositories.go:39,106](../../internal/infrastructure/persistence/postgres/repositories.go#L39)
- **Issue:** Both `GetByID` implementations use `if err == sql.ErrNoRows` rather than `if errors.Is(err, sql.ErrNoRows)`. The Go standard library and the project rules both require `errors.Is` for sentinel comparison so that wrapped errors are matched correctly.
- **Impact:** If any middleware, decorator, or future retry wrapper wraps the error before it reaches this comparison, the branch will be missed and `domain.ErrEventNotFound` / `domain.ErrOrderNotFound` will never be returned — callers will receive a raw driver error and return HTTP 500 instead of 404.
- **Fix:**
  ```go
  if errors.Is(err, sql.ErrNoRows) {
  ```

---

### [MEDIUM] `events_outbox` has no index on `(processed_at, id)` — polling scan is a full table scan at scale

- **File:** [deploy/postgres/migrations/000003_add_outbox.up.sql](../../deploy/postgres/migrations/000003_add_outbox.up.sql), [deploy/postgres/migrations/000005_add_outbox_processed_at.up.sql](../../deploy/postgres/migrations/000005_add_outbox_processed_at.up.sql)
- **Issue:** `ListPending` filters `WHERE processed_at IS NULL ORDER BY id ASC LIMIT $1`. There is no index covering `processed_at`. Once the outbox table grows (e.g. after months of high-volume events where rows are never hard-deleted), every poll tick issues a sequential scan.
- **Impact:** At sustained load the relay's 500 ms poll cycle will degrade, increasing latency of order-created Kafka events and eventually timing out.
- **Fix:** Add a migration (007) with:
  ```sql
  CREATE INDEX CONCURRENTLY idx_outbox_pending ON events_outbox (id)
    WHERE processed_at IS NULL;
  ```
  `CONCURRENTLY` avoids a table lock on production. Also consider a periodic hard-delete or archival job for rows older than N days.

---

### [MEDIUM] `provideDB` sets pool config _after_ a successful ping, and `ConnMaxLifetime` is never set

- **File:** [cmd/booking-cli/main.go:282-300](../../cmd/booking-cli/main.go#L282)
- **Issue:** `db.SetMaxOpenConns`, `SetMaxIdleConns`, and `SetConnMaxIdleTime` are called only after the ping loop succeeds. During the retry window (up to 10 seconds) the pool operates with Go's default limits (unlimited open connections). Additionally, `SetConnMaxLifetime` is never configured — connections can live indefinitely, which causes stale-connection errors after a Postgres-side `tcp_keepalives_idle` timeout or a load-balancer reset.
- **Impact:** Under a burst of connections during startup retry, the application could exhaust the Postgres `max_connections` limit. Long-running deploys accumulate idle connections that eventually produce `driver: bad connection` errors.
- **Fix:** Call all four setters immediately after `sql.Open`, before the ping loop. Add `ConnMaxLifetime` (e.g. `cfg.Postgres.MaxLifetime`, default `"30m"`) to `PostgresConfig`.

---

### [LOW] `pgAdvisoryLock.TryLock` silently swallows `acquired=false` with `err=nil` path

- **File:** [internal/infrastructure/persistence/postgres/advisory_lock.go:41-50](../../internal/infrastructure/persistence/postgres/advisory_lock.go#L41)
- **Issue:** When `pg_try_advisory_lock` returns `false` (lock held by another session), the code falls into `if err != nil || !acquired` and closes the connection. `err` is `nil` here, so only `!acquired` triggers. The function then returns `false, nil`. This is correct, but the comment on line 44 says "If we failed to acquire **or got an error**" — leading a reader to conflate two very different outcomes. When `err != nil` the current code also returns `false, nil` (because `err` is overwritten to `nil` on line 47 by the `if l.conn != nil` block, since `l.conn` is not nil at that point yet… actually the connection _was_ assigned to `l.conn` on line 38, so `_ = l.conn.Close(); l.conn = nil` runs, but `return false, err` where `err` is the original query error). Re-reading: the path _does_ return `false, err` for the query-error case. The confusion is in the comment and the dual-condition `if` making the code hard to audit.
- **Impact:** Maintenance risk; a future refactor might accidentally merge the two cases.
- **Fix:** Split into two explicit branches:
  ```go
  if err != nil {
      _ = conn.Close()
      l.conn = nil
      return false, fmt.Errorf("pg_try_advisory_lock: %w", err)
  }
  if !acquired {
      _ = conn.Close()
      l.conn = nil
      return false, nil
  }
  ```

---

### [LOW] Migration 001 seeds data — coupling schema migration with data migration

- **File:** [deploy/postgres/migrations/000001_create_events_table.up.sql:9](../../deploy/postgres/migrations/000001_create_events_table.up.sql#L9)
- **Issue:** `INSERT INTO events … VALUES ('Jay Chou Concert', 100, 100)` is embedded in the schema migration. Schema migrations should be idempotent DDL only; data seeding belongs in a separate seed script or fixture.
- **Impact:** Running `make migrate-up` on a production database inserts a test record. Rolling back and re-applying (e.g. during CI) creates duplicate rows if the table already exists but the migration tool does not track the run.
- **Fix:** Remove the `INSERT` from migration 001. Add a `deploy/postgres/seeds/seed_dev.sql` (not applied in production CI).

---

### [NIT] `_ = rows.Close()` discards close errors silently

- **File:** [internal/infrastructure/persistence/postgres/repositories.go:140](../../internal/infrastructure/persistence/postgres/repositories.go#L140)
- **Issue:** `defer func() { _ = rows.Close() }()` explicitly ignores the close error. A `rows.Close` error can surface connection-level problems (e.g. network interruption mid-result-set).
- **Fix:** Log the error at warn level:
  ```go
  defer func() {
      if cerr := rows.Close(); cerr != nil {
          // log or append to named return error
      }
  }()
  ```

---

### [NIT] `OutboxRepository.getExecutor` is defined on the struct, not in `uow.go` — breaks pattern symmetry

- **File:** [internal/infrastructure/persistence/postgres/repositories.go:211-216](../../internal/infrastructure/persistence/postgres/repositories.go#L211)
- **Issue:** `postgresEventRepository.getExecutor` and `postgresOrderRepository.getExecutor` are defined in `uow.go`, but `postgresOutboxRepository.getExecutor` is defined inline in `repositories.go`. Inconsistent placement.
- **Fix:** Move all three `getExecutor` implementations into `uow.go` for co-location with the `txKey` type.

---

## Test-Coverage Gaps (in-scope only)

- `advisory_lock.go`: No unit tests for `TryLock` when already holding the connection (the early-return path at L30), nor for `Unlock` when `l.conn == nil`.
- `repositories.go`: `ListOrders` with `status != nil` filter is untested at the repository layer (only the mock is exercised in service tests).
- `repositories.go`: `IncrementTicket` guard (`available_tickets + $2 <= total_tickets`) — no test verifying the over-increment case returns the sentinel error.
- `uow.go`: Nested `Do` (already-in-transaction path at L23) has no dedicated test; only exercised implicitly.
- Migration 006 partial index: no integration test verifying that a `failed` order allows a second purchase attempt for the same `(user_id, event_id)`.

---

## Follow-Up Questions

1. Is `GetByID` on `EventRepository` ever called outside a transaction in production paths? If so, the `FOR UPDATE` is always a no-op lock on an auto-commit connection — should it be moved to a dedicated `GetForUpdate` variant?
2. Are processed outbox rows ever hard-deleted, or do they accumulate indefinitely? The relay only marks `processed_at`; without a retention policy the table will grow unboundedly and negate the partial index benefit.
3. `ConnMaxLifetime` is absent from config. Is there a Postgres-side or proxy-side (e.g. PgBouncer) keepalive configured to compensate?
4. The `OrderStatusCompensated` idempotency check in `sagaCompensator` reads the order status inside the same `UoW` transaction as `IncrementTicket` + `UpdateStatus`. Is there a risk of a lost-update if two saga messages for the same order arrive concurrently and both read `status != compensated` before either commits?

---

## Out of Scope / Deferred

- Redis cache layer (`internal/infrastructure/cache/`) — covered by Dimension #5 (cache/messaging).
- Kafka consumer idempotency (`internal/infrastructure/messaging/`) — covered by Dimension #4.
- API-layer rate limiting and idempotency key TTL — covered by Dimension #1 (domain-application) / #2 (API).
- `domain.IdempotencyRepository` implementation in Redis — out of this dimension's scope (Redis, not Postgres).
