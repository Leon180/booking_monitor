-- We need to drop the existing unique constraint and replace it with a partial index
-- that only enforces uniqueness for active orders (not failed ones), allowing users
-- to retry purchasing a ticket if their first attempt failed during payment.

ALTER TABLE orders DROP CONSTRAINT IF EXISTS uq_orders_user_event;

CREATE UNIQUE INDEX uq_orders_user_event ON orders (user_id, event_id) WHERE status != 'failed';
