-- KEYS[1]: inventory_key (event:{id}:qty)
-- ARGV[1]: count

-- Restore inventory
redis.call("INCRBY", KEYS[1], ARGV[1])

return 1
