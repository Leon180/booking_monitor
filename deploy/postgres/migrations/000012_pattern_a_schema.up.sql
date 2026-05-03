-- 000012_pattern_a_schema.up.sql
-- D1 — Pattern A schema migration (reservation + payment + sections)
--
-- Adds the schema scaffolding for Phase 3a (Pattern A — Stripe Checkout /
-- KKTIX-style reservation flow). The application-layer state-machine
-- replacement (Pattern A: pending → awaiting_payment → paid | expired |
-- payment_failed) ships in D2; this migration is purely additive DDL so
-- D2 can land with a code-only diff.
--
-- This migration also adds `event_sections` and `orders.section_id`
-- per the section-level sharding architectural backlog entry
-- (`docs/architectural_backlog.md` § Section-level inventory sharding).
-- We intentionally bring this in as part of D1 — even though sharding
-- itself isn't implemented in Phase 3 — because shipping `section_id`
-- in the schema NOW makes future Layer 1 sharding a routing change
-- rather than a schema migration. This is a low-cost / high-future-value
-- design choice.
--
-- Decision summary (per docs/post_phase2_roadmap.md § Phase 3a):
--   1B — Section model: new `event_sections` table, FK from orders
--   2A — State machine: clean replacement (no DB CHECK; Go enforces)
--   3C — Reservation TTL: per-order `reserved_until` (fact) + per-event
--        `reservation_window_seconds` (admin-configurable default)
--   4B — payment_intent_id: nullable, set during POST /orders/:id/pay
--   5A — Backward compat: breaking change at API; schema is additive
--
-- What this migration deliberately does NOT do:
--
--   * No CHECK constraint on `orders.status`. Same rationale as 000010 —
--     status validity is enforced by `internal/domain/order.go::OrderStatus*`
--     + Mark* transitions. Adding DB-level CHECK forces every future
--     status value to land via two synchronized PRs (DB + Go) instead of one.
--
--   * No backfill of existing `orders.section_id` to a default value.
--     `section_id` is NULLable to tolerate the migration window. New code
--     paths (Pattern A) will INSERT with a non-NULL section_id; legacy
--     orders (created before this migration) keep NULL and remain
--     historically queryable. Application code that reads orders
--     should treat `section_id IS NULL` as "pre-Pattern-A legacy order".
--
--   * No FK constraint from `orders.section_id` to `event_sections.id`.
--     Same rationale as orders.event_id → events.id (also unconstrained):
--     this codebase chooses application-layer referential integrity over
--     DB-level FKs to avoid coupling tx ordering between the OutboxRelay
--     leader and the booking-write path. If this becomes a real concern,
--     introduce as a separate PR with a documented migration-window FK
--     state-check job.
--
--   * No INSERT of seed sections for existing events. The seed event
--     "Jay Chou Concert" from 000001 is dropped by 000008's CASCADE drop;
--     dev resets typically use `make reset-db` which seeds fresh data.
--     Production-shape data ships through the application's Pattern A
--     event-creation flow (D2-D3).
--
-- Why the new index on (status, reserved_until)
--
--   The reservation expiry sweeper (D6 — mirrors recon's structure)
--   scans for `status = 'awaiting_payment' AND reserved_until < NOW()`
--   every 30-60 seconds. Without an index, this is a full orders-table
--   scan per sweep. Partial index covers exactly the sweeper's working
--   set; index size stays tiny because awaiting_payment is a transient
--   state (orders move to paid/expired/payment_failed within the
--   reservation window).

------------------------------------------------------------------------
-- 1. event_sections — multi-section event support (sharding axis prep)
------------------------------------------------------------------------
CREATE TABLE event_sections (
    id UUID PRIMARY KEY,
    event_id UUID NOT NULL,
    name VARCHAR(255) NOT NULL,
    total_tickets INT NOT NULL,
    available_tickets INT NOT NULL,
    version INT DEFAULT 0,
    CONSTRAINT check_section_available_tickets_non_negative
        CHECK (available_tickets >= 0),
    CONSTRAINT uq_section_name_per_event
        UNIQUE (event_id, name)
);

-- Index for the common "fetch all sections of an event" query path
-- (event detail page, Pattern A reservation pre-check).
CREATE INDEX idx_event_sections_event_id ON event_sections (event_id);

------------------------------------------------------------------------
-- 2. events.reservation_window_seconds — admin-configurable TTL default
------------------------------------------------------------------------
-- Default 900s (15 minutes) — typical for Stripe Checkout / KKTIX flow.
-- DEFAULT NOW() at column level means existing rows get the value
-- without an explicit UPDATE backfill (PG 11+ fast-path for INT DEFAULT).
ALTER TABLE events
    ADD COLUMN reservation_window_seconds INT NOT NULL DEFAULT 900;

------------------------------------------------------------------------
-- 3. orders — Pattern A new columns
------------------------------------------------------------------------
ALTER TABLE orders
    ADD COLUMN section_id UUID NULL,
    ADD COLUMN reserved_until TIMESTAMPTZ NULL,
    ADD COLUMN payment_intent_id VARCHAR(255) NULL;

-- Partial index for the reservation expiry sweeper. Mirrors 000010's
-- partial-index pattern for recon.
CREATE INDEX idx_orders_awaiting_payment_reserved_until
    ON orders (status, reserved_until)
 WHERE status = 'awaiting_payment';

-- Partial index for "is this section_id in active use?" — supports the
-- future Layer 1 sharding router's per-section availability checks.
-- Partial because most orders are confirmed/paid (terminal); the active
-- working set is the small minority.
CREATE INDEX idx_orders_section_id_active
    ON orders (section_id, status)
 WHERE section_id IS NOT NULL
   AND status NOT IN ('paid', 'compensated', 'expired', 'payment_failed', 'confirmed', 'failed');
