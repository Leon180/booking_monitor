-- 000008_uuid_pk_migration.down.sql
-- Roll back PR 34's UUID PK migration: drop UUID-typed tables and
-- restore the prior SERIAL-typed schema verbatim. Because the up
-- migration is destructive, the down migration is also destructive
-- — it cannot recover any data lost in the up step.

DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS events_outbox CASCADE;
DROP TABLE IF EXISTS events CASCADE;

CREATE TABLE events (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    total_tickets INT NOT NULL,
    available_tickets INT NOT NULL,
    version INT DEFAULT 0,
    CONSTRAINT check_available_tickets_non_negative CHECK (available_tickets >= 0)
);

CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    event_id INT NOT NULL,
    user_id INT NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    status VARCHAR(50) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX uq_orders_user_event ON orders (user_id, event_id) WHERE status != 'failed';

CREATE TABLE events_outbox (
    id SERIAL PRIMARY KEY,
    event_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(20) DEFAULT 'PENDING',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    -- TIMESTAMPTZ matches migration 000005's original column type;
    -- a rollback must restore the prior shape exactly.
    processed_at TIMESTAMPTZ
);

CREATE INDEX events_outbox_pending_idx ON events_outbox (id) WHERE processed_at IS NULL;
