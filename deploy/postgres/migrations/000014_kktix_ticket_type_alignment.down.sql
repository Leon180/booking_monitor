-- 000014_kktix_ticket_type_alignment.down.sql
-- Reverse 000014 — restore the D1-era section vocabulary.
--
-- Operational note. Down migrations are tested via the
-- TestMigrationsRoundTrip integration test (CP4c, PR #65). They walk
-- every migration through up → down → up and assert schema-state
-- equality. If you change the up file, mirror it here.

------------------------------------------------------------------------
-- Reverse order: undo step 4, then 3, then 2, then 1.
------------------------------------------------------------------------

-- 4. Drop the new partial index, restore the D1 one.
DROP INDEX IF EXISTS idx_orders_ticket_type_id_active;

-- Cannot recreate the D1 index name yet — it referenced section_id
-- which still has the new ticket_type_id name at this point. We
-- recreate it after the column rename below.

-- 3. Drop the new orders columns, rename ticket_type_id back to section_id.
ALTER TABLE orders
    DROP COLUMN IF EXISTS amount_cents,
    DROP COLUMN IF EXISTS currency;

ALTER TABLE orders
    RENAME COLUMN ticket_type_id TO section_id;

-- Now recreate the D1 partial index with the original column name.
CREATE INDEX idx_orders_section_id_active
    ON orders (section_id, status)
 WHERE section_id IS NOT NULL
   AND status NOT IN ('paid', 'compensated', 'expired', 'payment_failed', 'confirmed', 'failed');

-- 2. Drop the KKTIX-shape columns from event_ticket_types.
ALTER TABLE event_ticket_types
    DROP COLUMN IF EXISTS price_cents,
    DROP COLUMN IF EXISTS currency,
    DROP COLUMN IF EXISTS sale_starts_at,
    DROP COLUMN IF EXISTS sale_ends_at,
    DROP COLUMN IF EXISTS per_user_limit,
    DROP COLUMN IF EXISTS area_label;

-- 1. Rename event_ticket_types → event_sections + restore constraint /
--    index names.
ALTER TABLE event_ticket_types
    RENAME CONSTRAINT check_ticket_type_available_tickets_non_negative
                   TO check_section_available_tickets_non_negative;

ALTER TABLE event_ticket_types
    RENAME CONSTRAINT uq_ticket_type_name_per_event
                   TO uq_section_name_per_event;

ALTER INDEX idx_event_ticket_types_event_id
        RENAME TO idx_event_sections_event_id;

ALTER TABLE event_ticket_types RENAME TO event_sections;
