-- golang-migrate: no-transaction

DROP INDEX CONCURRENTLY IF EXISTS events_outbox_pending_idx;
