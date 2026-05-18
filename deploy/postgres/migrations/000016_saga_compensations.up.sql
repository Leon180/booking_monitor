CREATE TABLE IF NOT EXISTS saga_compensations (
    compensation_id   VARCHAR(64)  PRIMARY KEY,
    order_id          UUID         NOT NULL REFERENCES orders(id),
    completed_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    redis_reverted_at TIMESTAMPTZ
    -- NULL  = DB compensation done; Redis revert pending (safe to retry)
    -- NOT NULL = fully completed; skip Redis revert on retry
);

CREATE INDEX saga_compensations_order_id_idx ON saga_compensations(order_id);
