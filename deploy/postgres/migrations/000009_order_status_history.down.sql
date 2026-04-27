-- 000009_order_status_history.down.sql
-- Drops the audit log table. Indexes are dropped automatically with
-- the table. Pre-production schema — no data migration concerns; in
-- a production rollback, audit data would need to be archived first.

DROP TABLE IF EXISTS order_status_history;
