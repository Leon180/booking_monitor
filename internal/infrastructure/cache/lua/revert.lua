-- KEYS[1]: inventory_key (event:{id}:qty)
-- KEYS[2]: buyers_key (event:{id}:buyers)
-- ARGV[1]: user_id
-- ARGV[2]: count

-- 1. Remove user from buyers list
redis.call("SREM", KEYS[2], ARGV[1])

-- 2. Restore inventory
redis.call("INCRBY", KEYS[1], ARGV[2])

return 1
