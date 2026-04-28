-- 000010_charging_intent_log.down.sql
-- Reverse of 000010. Drop index first (depends on the column), then
-- the column itself. Down is meant for development / rollback only;
-- a production rollback after A4 has shipped would lose all
-- Charging-state orders' last-touched timestamps.

DROP INDEX IF EXISTS idx_orders_status_updated_at_partial;

ALTER TABLE orders
    DROP COLUMN IF EXISTS updated_at;
