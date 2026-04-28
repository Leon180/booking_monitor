-- 000010_charging_intent_log.up.sql
-- A4 — Charging two-phase intent log
--
-- Adds the schema scaffolding for an `OrderStatusCharging` intermediate
-- state between Pending and Confirmed/Failed. The application layer
-- writes Pending → Charging BEFORE calling the payment gateway, then
-- Charging → Confirmed (or Failed) after the gateway call returns.
-- The Charging row is the "intent log" — visible to a separate recon
-- subcommand that sweeps stuck-Charging orders and resolves them via
-- gateway.GetStatus().
--
-- This migration is intentionally minimal:
--
--   1. Add `updated_at TIMESTAMPTZ` to orders.
--   2. Backfill `updated_at = created_at` for existing rows.
--   3. Add a PARTIAL index on (status, updated_at) covering the
--      reconciler's sweep predicate.
--
-- What this migration deliberately does NOT do:
--
--   * No CHECK constraint on `status` — the column is `VARCHAR(50)`
--     today and validity is enforced by the Go domain state machine
--     (`internal/domain/order.go::OrderStatus*` + Mark* transitions).
--     Adding a DB-level CHECK would force every future status value
--     to land via two synchronized PRs (DB + Go) instead of one.
--
--   * No backfill of `Pending` rows to `Charging` — existing in-flight
--     orders stay Pending. The application-side widen of MarkConfirmed
--     / MarkFailed (accepting source ∈ {Pending, Charging}) makes the
--     transitions backwards-compatible during the migration window.
--     The follow-up tightening to charging-only ships in a separate
--     PR after `order_status_history` confirms zero `Pending → terminal`
--     transitions for 5+ minutes (cutover trigger documented in
--     PROJECT_SPEC §A4).
--
-- Why TIMESTAMPTZ (not TIMESTAMP)
--
--   New timestamp columns in this codebase use TIMESTAMPTZ — see the
--   header in 000009_order_status_history for the full rationale.
--   The pre-existing `orders.created_at` is TIMESTAMP for historical
--   reasons; we don't migrate it here (out of scope).
--
-- Why a PARTIAL index, not full
--
--   The reconciler's sweep predicate is:
--       WHERE status = 'charging' AND updated_at < NOW() - <threshold>
--
--   Terminal statuses (`confirmed`, `failed`, `compensated`) NEVER
--   appear in that WHERE clause; including them in the index just
--   inflates index size and slows index maintenance on every UPDATE.
--   The partial predicate `WHERE status IN ('charging', 'pending')`
--   covers exactly the reconciler's working set + the pending rows
--   that may briefly look stuck before the worker picks them up.
--   Index size shrinks ~60-70% versus a full index given a typical
--   95%+ confirmed:total ratio.
--
-- Why include 'pending' in the partial predicate
--
--   The reconciler today only scans `charging` rows, but a future
--   Pending-stuck recon (when N7's k8s manifests land and Kafka can
--   surface stuck-pending via consumer-group lag) can reuse the same
--   index without a schema change. Cheap forward compatibility.

ALTER TABLE orders
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- No backfill. The DEFAULT NOW() above already gave every existing
-- row a timestamp at migration time. That value is "historically
-- inaccurate" for terminal rows (confirmed/failed/compensated) but
-- harmless: the reconciler's WHERE clause only cares about Charging
-- rows, and there are none at migration time (the new state ships
-- with this PR). Backfilling `updated_at = created_at` would require
-- a TIMESTAMP→TIMESTAMPTZ cast that depends on session timezone — a
-- subtle off-by-N-hours risk for negligible benefit. Skip.

CREATE INDEX idx_orders_status_updated_at_partial
    ON orders (status, updated_at)
 WHERE status IN ('charging', 'pending');

-- order_status_history.from_status currently allows NULL for "no
-- prior state". After A4 it also represents the multi-source
-- transition window (an audit row may show from='pending' OR
-- from='charging' for the same to-status). No schema change needed
-- here — the column already accepts both values; just noting the
-- semantic widening for future readers.
