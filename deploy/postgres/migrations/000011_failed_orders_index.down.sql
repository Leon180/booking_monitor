-- 000011_failed_orders_index.down.sql
-- Revert the widened partial index back to the 000010 form.

DROP INDEX IF EXISTS idx_orders_status_updated_at_partial;

CREATE INDEX idx_orders_status_updated_at_partial
    ON orders (status, updated_at)
 WHERE status IN ('charging', 'pending');
