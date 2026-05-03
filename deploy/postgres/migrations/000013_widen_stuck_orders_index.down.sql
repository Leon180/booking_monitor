-- 000013_widen_stuck_orders_index.down.sql
-- Reverse 000013 — narrow the partial index back to 000011's predicate.
-- Same DROP + CREATE pattern.

DROP INDEX IF EXISTS idx_orders_status_updated_at_partial;

CREATE INDEX idx_orders_status_updated_at_partial
    ON orders (status, updated_at)
 WHERE status IN ('charging', 'pending', 'failed');
