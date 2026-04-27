-- 000009_order_status_history.up.sql
-- Append-only audit table that records every state-machine transition
-- on `orders`. Pairs with the typed Mark{Confirmed,Failed,Compensated}
-- methods landed in PR #39: each transition writes one row here in
-- the same SQL statement as the orders.status UPDATE (CTE-based,
-- atomic — see postgres/repositories.go::transitionStatus).
--
-- Design notes (senior-review-friendly):
--
--   * BIGSERIAL PK over UUID v7: the table is append-only and most
--     queries are "latest N for this order" or "what happened in this
--     time range" — both prefer a sequential numeric PK over a
--     time-prefixed UUID for index size and scan cost. UUID elsewhere
--     in the schema is for client-assigned aggregate IDs; this is a
--     server-generated audit log row id, which is a different concern.
--
--   * `from_status` allows NULL for two reasons: (a) future support
--     for recording the initial Pending creation as a transition
--     (NULL → Pending), and (b) it documents that "no prior state"
--     is a real value, not an error.
--
--   * `ON DELETE CASCADE`: if an order row is ever deleted, its
--     history goes too — keeps referential integrity. Dropping
--     orders is not a normal operation in this system; this is
--     defensive against test cleanup, manual ops, or future
--     migration paths.
--
--   * TIMESTAMPTZ for `occurred_at`: timezone-aware so cross-region
--     reads don't drift. The mistake from 000008's review on
--     `processed_at` is not repeated here.
--
--   * Two indexes serve the two query patterns:
--       - (order_id, occurred_at) — "show this order's history"
--         (most common UI / debug query)
--       - (occurred_at)            — "what transitions happened in
--         this time window" (operational dashboards, alerting on
--         stuck-Pending orders)
--
--   * The table is APPEND-ONLY by discipline, not by hard CHECK.
--     A trigger forbidding UPDATE/DELETE was considered but rejected
--     for v1: tests + reset-db need TRUNCATE, and adding a trigger
--     requires teaching ops to disable it for those flows. If a
--     compliance audit requires hard append-only, add the trigger
--     in a follow-up — schema is already correct.

CREATE TABLE order_status_history (
    id          BIGSERIAL    PRIMARY KEY,
    order_id    UUID         NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    from_status VARCHAR(20),
    to_status   VARCHAR(20)  NOT NULL,
    occurred_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_order_status_history_order_id_occurred
    ON order_status_history (order_id, occurred_at);

CREATE INDEX idx_order_status_history_occurred
    ON order_status_history (occurred_at);
