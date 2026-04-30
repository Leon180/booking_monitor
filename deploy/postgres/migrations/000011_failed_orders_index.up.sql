-- 000011_failed_orders_index.up.sql
-- A5 — Saga watchdog: widen the partial index to cover stuck-Failed sweeps.
--
-- Background: migration 000010 created `idx_orders_status_updated_at_partial`
-- with predicate `WHERE status IN ('charging', 'pending')` for the
-- A4 reconciler. The A5 watchdog sweeps stuck-Failed rows with the
-- same query shape (`WHERE status = X AND updated_at < NOW() - $1`),
-- so the cleanest move is to widen the existing partial predicate
-- rather than create a parallel single-status index.
--
-- Why widen instead of separate index:
--
--   * One index, one set of maintenance overhead on UPDATEs that flip
--     status. Two single-status partial indices would each take a
--     write hit on every transition that touches their predicate.
--
--   * Compose-able predicates. A future index user (status='X' for any
--     of the three) gets the same partial coverage with a single
--     plan-cache entry.
--
--   * Postgres can use the partial index for any predicate that
--     subsumes the partial WHERE clause. `WHERE status='failed'` is
--     subsumed by `WHERE status IN ('charging','pending','failed')`,
--     so the watchdog query plans cleanly without any additional
--     index hint.
--
-- What this migration does NOT do:
--
--   * No CHECK constraint on `status` — same rationale as 000010
--     (validity is enforced by the Go domain state machine).
--
--   * No reindex / rebuild of unrelated indexes.
--
-- Why DROP + CREATE instead of REINDEX
--
--   Postgres has no DDL to rewrite a partial index's WHERE predicate
--   in place. The DROP + CREATE pair is the only path. CREATE INDEX
--   is non-blocking with `CONCURRENTLY` but cannot run inside a
--   transaction — and the migration tool wraps each step in a tx.
--   For the size of `orders` at our current scale (sub-millions),
--   the brief lock window during a non-concurrent CREATE INDEX is
--   acceptable. If the table grows to billions, the future migration
--   should be split into two CONCURRENTLY-issued statements outside
--   the migration tool's transaction wrapper.
--
-- Why include 'failed' AND keep 'charging','pending'
--
--   The reconciler still scans 'charging'; future Pending-stuck recon
--   (deferred from 000010) still wants 'pending'. Removing either
--   would regress an existing query plan. Strictly additive.

DROP INDEX IF EXISTS idx_orders_status_updated_at_partial;

CREATE INDEX idx_orders_status_updated_at_partial
    ON orders (status, updated_at)
 WHERE status IN ('charging', 'pending', 'failed');
