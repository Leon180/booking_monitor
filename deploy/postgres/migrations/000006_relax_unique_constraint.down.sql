-- Clean up old failed orders to prevent unique constraint violations on down
DELETE FROM orders WHERE status = 'failed';

DROP INDEX IF EXISTS uq_orders_user_event;

ALTER TABLE orders ADD CONSTRAINT uq_orders_user_event UNIQUE (user_id, event_id);
