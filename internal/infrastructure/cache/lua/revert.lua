-- KEYS[1]: inventory_key (event:{id}:qty)
-- KEYS[2]: idempotency_key (saga:reverted:{compensation_id})
-- ARGV[1]: count

if redis.call("SETNX", KEYS[2], "1") == 1 then
    -- Set an expiration for the idempotency key (7 days)
    redis.call("EXPIRE", KEYS[2], 604800)
    
    local current = redis.call("GET", KEYS[1])
    if current then
        redis.call("INCRBY", KEYS[1], ARGV[1])
    end
    return 1
else
    return 0 -- Already reverted
end
