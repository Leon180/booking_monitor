-- 000014_kktix_ticket_type_alignment.up.sql
-- D4.1 — KKTIX-aligned ticket type model + price snapshot
--
-- Background. D4 (PR #87) surfaced an architectural smell — payment.Service
-- had `priceCents` / `currency` as struct fields populated from
-- BookingConfig, meaning every event in the system had the same price.
-- Real ticketing systems put price + sale rules on the ticket_type
-- entity (KKTIX 票種 model). This migration retrofits that.
--
-- D1 (000012) introduced `event_sections` + `orders.section_id` as
-- forward-compat schema for "section as inventory + sharding axis".
-- That schema was added but never wired through Go application code —
-- only the DDL exists in production. D4.1 takes that dormant schema and:
--
--   1. Renames `event_sections` → `event_ticket_types` (KKTIX vocabulary)
--   2. Adds price_cents + currency + sale_starts_at + sale_ends_at
--      + per_user_limit + area_label (KKTIX feature parity, schema-only;
--      sale_window / per_user_limit are forward-compat for D8+)
--   3. Renames `orders.section_id` → `orders.ticket_type_id`
--   4. Adds `orders.amount_cents` + `orders.currency` for the
--      industry-standard "snapshot price at book time" pattern (D4.1
--      eliminates re-querying ticket_type at /pay time)
--
-- Decision references:
--   docs/design/ticket_pricing.md  (§5 schema choice; §6 snapshot rationale)
--   docs/design/inventory_hold.md  (order = hold pattern this builds on)
--   docs/architectural_backlog.md  § KKTIX-aligned ticket type model
--
-- What this migration does NOT do:
--   * Does NOT rename Redis Lua keys (`event:{id}:qty` stays). D4.1
--     assumes 1 ticket_type per event so the key path is equivalent.
--     D8 multi-ticket-type-per-event will rename to
--     `ticket_type:{id}:qty` along with the broader Lua / rehydrate /
--     drift-detector changes.
--   * Does NOT add a CHECK constraint on `currency`. ISO 4217 validation
--     happens at the domain factory; same rationale as `orders.status`
--     not having a CHECK (Go enforces, DB doesn't).
--   * Does NOT backfill `orders.amount_cents` for legacy orders. They
--     stay NULL — D3-era orders predate price snapshot. New orders
--     (D4.1+) always have it set by BookingService.
--   * Does NOT add an FK from `orders.ticket_type_id` to
--     `event_ticket_types.id`. Same rationale as D1's missing FK on
--     section_id (application-layer referential integrity to avoid
--     cross-aggregate locking on the OutboxRelay path).

------------------------------------------------------------------------
-- 1. Rename event_sections → event_ticket_types
------------------------------------------------------------------------

ALTER TABLE event_sections RENAME TO event_ticket_types;

-- Rename child constraints + index to keep pg_dump output aligned with
-- the new vocabulary. Cosmetic but worth doing while we're touching
-- the table.
ALTER TABLE event_ticket_types
    RENAME CONSTRAINT check_section_available_tickets_non_negative
                   TO check_ticket_type_available_tickets_non_negative;

ALTER TABLE event_ticket_types
    RENAME CONSTRAINT uq_section_name_per_event
                   TO uq_ticket_type_name_per_event;

ALTER INDEX idx_event_sections_event_id
        RENAME TO idx_event_ticket_types_event_id;

------------------------------------------------------------------------
-- 2. Add KKTIX-shape columns to event_ticket_types
------------------------------------------------------------------------

ALTER TABLE event_ticket_types
    ADD COLUMN price_cents     BIGINT      NOT NULL DEFAULT 2000,
    ADD COLUMN currency        VARCHAR(3)  NOT NULL DEFAULT 'usd',
    ADD COLUMN sale_starts_at  TIMESTAMPTZ NULL,
    ADD COLUMN sale_ends_at    TIMESTAMPTZ NULL,
    ADD COLUMN per_user_limit  INT         NULL,
    ADD COLUMN area_label      VARCHAR(255) NULL;

-- price_cents DEFAULT 2000 (= US$20) matches the ex-BookingConfig
-- default. The DEFAULT exists only to backfill existing rows during
-- ALTER (production currently has 0 rows in this table; defensive
-- safety net). New rows created via event.Service.CreateEvent always
-- set price explicitly via the domain factory invariant.
--
-- sale_starts_at / sale_ends_at / per_user_limit are SCHEMA-ONLY for D4.1.
-- The Go domain factory accepts them, the DTO surfaces them, but BookTicket
-- doesn't enforce sale window / quantity limit yet — that's D8 business-rule
-- work. Schema-first means D8 lands as code-only without another migration.
--
-- area_label captures the legacy 「分區」 metadata (e.g. "VIP A 區") that
-- D1's `event_sections.name` was implicitly trying to be. After D4.1, name
-- = ticket_type label ("VIP 早鳥票"); area_label = optional physical-area
-- annotation. D8's seat layer will reference area_label for grouping.

------------------------------------------------------------------------
-- 3. orders — rename section_id → ticket_type_id, add snapshot columns
------------------------------------------------------------------------

ALTER TABLE orders
    RENAME COLUMN section_id TO ticket_type_id;

ALTER TABLE orders
    ADD COLUMN amount_cents BIGINT     NULL,
    ADD COLUMN currency     VARCHAR(3) NULL;

-- amount_cents and currency are NULLable to tolerate legacy rows (D3
-- and earlier). New orders always have them set by BookingService.
-- A future migration could backfill (amount_cents = 0, currency = 'usd')
-- and tighten to NOT NULL, but only after a clean cutover window.

------------------------------------------------------------------------
-- 4. Update partial indexes referencing the renamed column
------------------------------------------------------------------------

-- D1 created idx_orders_section_id_active. Same intent (active orders
-- by section/ticket_type), updated column name.
DROP INDEX IF EXISTS idx_orders_section_id_active;

CREATE INDEX idx_orders_ticket_type_id_active
    ON orders (ticket_type_id, status)
 WHERE ticket_type_id IS NOT NULL
   AND status NOT IN ('paid', 'compensated', 'expired', 'payment_failed', 'confirmed', 'failed');
