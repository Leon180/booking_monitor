-- 000015_orders_payment_intent_id_unique_idx.down.sql
-- Drop the partial UNIQUE index from 000015.up.

DROP INDEX IF EXISTS orders_payment_intent_id_unique_idx;
