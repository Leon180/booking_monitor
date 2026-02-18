-- KEYS[1]: inventory_key (event:{id}:qty)
-- ARGV[1]: count
-- ARGV[2]: event_id
-- ARGV[3]: user_id (passed through to stream only)

-- 1. Deduct inventory atomically
local new_val = tonumber(redis.call("DECRBY", KEYS[1], ARGV[1]))

-- 2. If inventory went negative, revert and return sold out
if new_val < 0 then
    redis.call("INCRBY", KEYS[1], ARGV[1])
    return -1 -- Sold Out
end

-- 3. Publish to Stream
redis.call("XADD", "orders:stream", "*", "user_id", ARGV[3], "event_id", ARGV[2], "quantity", ARGV[1])

return 1 -- Success
