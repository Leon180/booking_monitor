-- KEYS[1]: inventory_key (ticket_type_qty:{id})
-- KEYS[2]: idempotency_key (saga:reverted:{compensation_id})
-- ARGV[1]: count
--
-- DUAL-NAMESPACE NOTE (PR #126 A7): KEYS[2]'s `compensation_id`
-- arrives in TWO shapes from TWO callers:
--
--   * worker path: compensation_id = rawMsg.ID — a Redis Streams
--     entry id like "1715000000-0" (parse-fail compensation in
--     `redis_queue.go::handleParseFailure`). Bounds idempotency
--     to "per failed stream-delivery attempt".
--
--   * saga path: compensation_id = "order:" + orderID (UUID) —
--     "order:01234567-89ab-cdef-...". Bounds idempotency to
--     "per saga-compensated order, across all retries".
--
-- The two shapes DO NOT collide in practice (stream-id shape is
-- "<ms>-<seq>", order shape is "order:<uuid>"), but the dual usage
-- is implicit. The Go-side interface is `RevertInventory(...,
-- compensationID string)` — both callers pass through the same
-- parameter, so this lua sees both string forms. Re-prefixing
-- (worker:msg:* / saga:order:*) at the call sites would make the
-- namespace explicit but breaks the 7-day TTL key fingerprints
-- already in flight; documented here instead.
--
-- Reverts Redis inventory for a compensated order. Idempotent via an
-- EXISTS check on the idempotency key rather than SETNX so we can reason
-- about crash semantics precisely:
--
-- 1. The entire script runs atomically inside Redis (single-threaded
--    Lua execution), so no concurrent caller can interleave.
-- 2. If Redis dies mid-script with appendfsync=everysec (our default),
--    the whole script's effects are lost together — retry is safe.
-- 3. If Redis dies mid-script with appendfsync=always, the INCRBY may
--    be persisted without the SET. On retry, EXISTS still returns 0 so
--    INCRBY runs again — that produces a double-revert, which the
--    booking system DETECTS (inventory > total) and alerts on, whereas
--    the previous `SETNX-then-INCRBY` design produced a SILENT-SKIP
--    under the same crash (key set, inventory never restored, no
--    operator signal). Loud over-revert beats silent under-revert.
--
-- The SET uses `EX` for atomic key+TTL (Redis ≥ 2.6.12) to replace the
-- previous `SETNX` + `EXPIRE` pair, which also fixes action-list L1.
-- NX is intentionally omitted: the EXISTS guard at the top of the script
-- already handles idempotency, and within an atomic Lua execution NX
-- would be redundant.

if redis.call("EXISTS", KEYS[2]) == 1 then
    return 0  -- Already reverted, nothing to do.
end

local current = redis.call("GET", KEYS[1])
if current then
    redis.call("INCRBY", KEYS[1], ARGV[1])
end

redis.call("SET", KEYS[2], "1", "EX", 604800)
return 1
