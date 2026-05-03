-- 000013_widen_stuck_orders_index.up.sql
-- D2 — widen `idx_orders_status_updated_at_partial` to cover Pattern A
-- failure-terminal states (`expired`, `payment_failed`).
--
-- Background: migration 000011 widened the partial index to cover
-- `('charging', 'pending', 'failed')` so the A5 saga watchdog could
-- share the A4 reconciler's index for `FindStuckFailed`. After D2
-- (Pattern A state machine), `MarkCompensated` accepts source ∈
-- {Failed, Expired, PaymentFailed} and the watchdog's
-- `FindStuckFailed` query is widened correspondingly. The partial
-- index needs to grow to match — otherwise the watchdog's query
-- against `WHERE status IN ('failed', 'expired', 'payment_failed')`
-- falls back to a sequential table scan once D5/D6 produce the new
-- failure states.
--
-- The widening lands NOW (with D2) so when D5 / D6 add code paths
-- that produce `expired` / `payment_failed` orders, the watchdog
-- already has efficient query support — no orphan window where the
-- watchdog scans the entire `orders` table to find stuck Pattern A
-- failures.
--
-- Same DROP + CREATE pattern as 000011 — Postgres has no DDL to
-- rewrite a partial index's WHERE predicate in place. For the
-- current scale of `orders`, the brief lock window during a
-- non-concurrent CREATE INDEX is acceptable. At billion-row scale,
-- split into CREATE INDEX CONCURRENTLY + DROP INDEX outside the
-- migration tool's tx wrapper.
--
-- What this migration does NOT do:
--   * No CHECK constraint on `status` — same rationale as 000010.
--   * No reindex of unrelated indexes.
--
-- Why include all 5 statuses (not just add the 2 new ones):
--   The reconciler still scans `charging` + `pending`; the watchdog
--   scans `failed` + the new `expired` / `payment_failed`. Removing
--   any of the existing predicates would regress an existing query
--   plan. Strictly additive.

DROP INDEX IF EXISTS idx_orders_status_updated_at_partial;

CREATE INDEX idx_orders_status_updated_at_partial
    ON orders (status, updated_at)
 WHERE status IN ('charging', 'pending', 'failed', 'expired', 'payment_failed');
