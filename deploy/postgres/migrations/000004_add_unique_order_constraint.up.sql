ALTER TABLE orders ADD CONSTRAINT uq_orders_user_event UNIQUE (user_id, event_id);
