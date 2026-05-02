-- KEYS[1]: inventory_key (event:{id}:qty:{shard}) — caller picks the shard
-- KEYS[2]: idempotency_key (saga:reverted:{compensation_id})
-- ARGV[1]: count
--
-- B3 sharding note: KEYS[1] now embeds a shard id (e.g.
-- event:abc-…-123:qty:2). The caller (saga compensator) MUST revert
-- to the same shard the original deduct landed in — incrementing the
-- wrong shard leaves one shard accumulating tickets above its allocation
-- cap. With INVENTORY_SHARDS=1 (default in B3.1 scaffolding), there is
-- only one shard so this is byte-equivalent to pre-B3 behavior.
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
