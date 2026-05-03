-- 000012_pattern_a_schema.down.sql
-- Reverse 000012 — drop Pattern A scaffolding cleanly.
--
-- Order matters: drop indexes first (some have predicates that reference
-- columns we're about to drop), then drop columns, then drop the table.
-- Keeps each individual statement valid against intermediate states.

------------------------------------------------------------------------
-- 1. Drop indexes that reference columns being removed
------------------------------------------------------------------------
DROP INDEX IF EXISTS idx_orders_section_id_active;
DROP INDEX IF EXISTS idx_orders_awaiting_payment_reserved_until;

------------------------------------------------------------------------
-- 2. Drop new columns from orders
------------------------------------------------------------------------
ALTER TABLE orders
    DROP COLUMN IF EXISTS payment_intent_id,
    DROP COLUMN IF EXISTS reserved_until,
    DROP COLUMN IF EXISTS section_id;

------------------------------------------------------------------------
-- 3. Drop reservation_window_seconds from events
------------------------------------------------------------------------
ALTER TABLE events
    DROP COLUMN IF EXISTS reservation_window_seconds;

------------------------------------------------------------------------
-- 4. Drop event_sections table (and its indexes implicitly)
------------------------------------------------------------------------
DROP TABLE IF EXISTS event_sections;
