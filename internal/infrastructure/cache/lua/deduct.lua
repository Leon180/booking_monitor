-- KEYS[1]: inventory_key (event:{id}:qty:{shard}) — caller picks the shard
-- ARGV[1]: count
-- ARGV[2]: event_id
-- ARGV[3]: user_id     (passed through to stream only)
-- ARGV[4]: order_id    (caller-minted UUIDv7; passed through to stream only)
-- ARGV[5]: shard       (the shard id KEYS[1] resolves to; emitted in stream
--                       so worker → outbox → saga compensator can revert
--                       to the same shard the deduct landed in)
--
-- B3 sharding note: the inventory key name in KEYS[1] embeds the shard
-- id (e.g. event:abc-…-123:qty:2). Go-side picks the shard before
-- calling this script and retries on alternate shards if -1 is returned
-- (depleted-shard != event-sold-out when N>1). The shard id flows into
-- the stream payload so revert can reconcile to the correct shard.

-- 1. Deduct inventory atomically (single shard)
local new_val = tonumber(redis.call("DECRBY", KEYS[1], ARGV[1]))

-- 2. If THIS shard's inventory went negative, revert and return sold out
--    for this shard. Caller decides whether to retry on a different shard
--    or surface a global sold-out to the user.
if new_val < 0 then
    redis.call("INCRBY", KEYS[1], ARGV[1])
    return -1 -- Shard sold out
end

-- 3. Publish to Stream — order_id + shard flow end-to-end so worker can
--    reuse the caller's id (instead of generating a fresh one on every
--    PEL retry) AND so saga compensation reverts to the same shard.
redis.call("XADD", "orders:stream", "*",
    "order_id", ARGV[4],
    "user_id",  ARGV[3],
    "event_id", ARGV[2],
    "quantity", ARGV[1],
    "shard",    ARGV[5])

return 1 -- Success
