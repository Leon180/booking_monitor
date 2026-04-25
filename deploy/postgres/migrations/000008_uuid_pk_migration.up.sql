-- 000008_uuid_pk_migration.up.sql
-- Migrate primary keys + foreign keys from SERIAL int to UUID for the
-- three core aggregates: events, orders, events_outbox.
--
-- Rationale:
--   * Domain-driven: aggregate identity should be assigned at construction
--     by the domain factory, not by the database. This is impossible with
--     SERIAL (DB always assigns) but trivial with client-generated UUIDv7.
--   * UUIDv7 is time-prefixed (RFC 9562, May 2024) so B-tree indexes still
--     append in roughly chronological order — performance matches BIGINT
--     within ~2% in benchmark, dramatically better than UUIDv4's random
--     distribution. See memory/uuid_v7_research.md.
--
-- Scope:
--   * orders.id, orders.event_id  -> UUID
--   * orders.user_id              STAYS INT (external user reference; this
--                                  service does not own the users table)
--   * events.id                   -> UUID
--   * events_outbox.id            -> UUID
--
-- Operational note: this migration is destructive. Run only on environments
-- where dropping existing rows is acceptable (dev / staging). For production
-- migration with data, the recommended sequence is:
--   1. Add new uuid columns alongside existing int columns
--   2. Backfill via scripted UUID generation per row
--   3. Update FKs in two phases (add new FK columns, drop old)
--   4. Swap PKs in a single transaction
-- The simple drop-and-recreate below is intentional for this codebase's
-- pre-production stage.

-- Drop FK + dependent tables first (orders depends on events; outbox is
-- independent but recreated for consistency).
DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS events_outbox CASCADE;
DROP TABLE IF EXISTS events CASCADE;

CREATE TABLE events (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    total_tickets INT NOT NULL,
    available_tickets INT NOT NULL,
    version INT DEFAULT 0,
    CONSTRAINT check_available_tickets_non_negative CHECK (available_tickets >= 0)
);

CREATE TABLE orders (
    id UUID PRIMARY KEY,
    event_id UUID NOT NULL,
    user_id INT NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    status VARCHAR(50) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Re-create the partial unique index for active orders (carried over
-- from migration 000006). Matches the same predicate.
CREATE UNIQUE INDEX uq_orders_user_event ON orders (user_id, event_id) WHERE status != 'failed';

CREATE TABLE events_outbox (
    id UUID PRIMARY KEY,
    event_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(20) DEFAULT 'PENDING',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    processed_at TIMESTAMP
);

-- Re-create the partial index for relay polling (carried over from 000007).
CREATE INDEX events_outbox_pending_idx ON events_outbox (id) WHERE processed_at IS NULL;
