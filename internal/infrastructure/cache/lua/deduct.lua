-- KEYS[1]: inventory_key (event:{id}:qty)
-- KEYS[2]: buyers_key (event:{id}:buyers)
-- ARGV[1]: user_id
-- ARGV[2]: count (should be 1 per requirement)

-- 1. Check if user already bought
if redis.call("SISMEMBER", KEYS[2], ARGV[1]) == 1 then
    return -2 -- Code for "Duplicate Purchase"
end

-- 2. Check inventory
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
if current < tonumber(ARGV[2]) then
    return -1 -- Code for "Sold Out"
end

-- 3. Deduct & Record
redis.call("DECRBY", KEYS[1], ARGV[2])
redis.call("SADD", KEYS[2], ARGV[1])

-- 4. Publish to Stream (Atomic)
-- ARGV[3]: event_id
redis.call("XADD", "orders:stream", "*", "user_id", ARGV[1], "event_id", ARGV[3], "quantity", ARGV[2])

return 1 -- Success
