-- Partial index to speed up OutboxRelay.ListPending.
--
-- The relay poll query is:
--   SELECT id, event_type, payload, status FROM events_outbox
--    WHERE processed_at IS NULL
--    ORDER BY id ASC
--    LIMIT $1;
--
-- Without this index, each poll does a full table scan of events_outbox
-- and heap-filters the WHERE clause. Under steady state most rows are
-- already processed and the unprocessed set is small, so a partial
-- index on the pending subset is both tiny and fast to maintain.
--
-- CREATE INDEX CONCURRENTLY can't run inside a transaction — but
-- golang-migrate defaults to running each migration in a tx. Tag this
-- file with `-- golang-migrate: no-transaction` so the runner issues
-- the CREATE INDEX outside a tx. If the migrate tool you're using
-- doesn't honour that pragma, fall back to the plain CREATE INDEX form
-- below (it blocks writes briefly on a large table — acceptable during
-- a maintenance window).
-- golang-migrate: no-transaction

CREATE INDEX CONCURRENTLY IF NOT EXISTS events_outbox_pending_idx
    ON events_outbox (id)
    WHERE processed_at IS NULL;
