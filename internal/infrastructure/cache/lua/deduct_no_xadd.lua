-- KEYS[1]: inventory_key (ticket_type_qty:{id})
-- KEYS[2]: metadata_key  (ticket_type_meta:{id})
-- ARGV[1]: count
--
-- Stage 5 variant of deduct.lua. Identical to deduct.lua EXCEPT:
--
--   * Does NOT XADD to orders:stream
--   * Returns metadata directly so the application can build the
--     message itself and publish to Kafka (with acks=all)
--
-- Rationale: Stage 5 (Damai-aligned) moves the durable queue from
-- ephemeral Redis Stream to replicated Kafka. The Lua keeps the
-- inventory atomicity (DECRBY + condition check + amount calc) but
-- delegates message durability to Kafka instead of issuing XADD.
-- The application layer publishes after Lua success; on Kafka
-- publish failure the application calls revert.lua to re-add the
-- inventory.
--
-- This script is loaded alongside deduct.lua at startup. Stage 5
-- binary uses this SHA, Stages 2-4 keep using deduct.lua's SHA.
-- See docs/d12/README.md for the per-stage architecture matrix.

-- 1. Deduct inventory atomically
local new_val = tonumber(redis.call("DECRBY", KEYS[1], ARGV[1]))

-- 2. If inventory went negative, revert and return sold out
if new_val < 0 then
    redis.call("INCRBY", KEYS[1], ARGV[1])
    return {"sold_out"}
end

-- 3. Load immutable booking metadata. Missing metadata is a rolling
--    cache-repair path: restore qty before returning the special code.
local meta = redis.call("HMGET", KEYS[2], "event_id", "price_cents", "currency")
local event_id = meta[1]
local price_cents_str = meta[2]
local currency = meta[3]
if not event_id or event_id == false or not price_cents_str or price_cents_str == false or not currency or currency == false then
    redis.call("INCRBY", KEYS[1], ARGV[1])
    return {"metadata_missing"}
end

local price_cents = tonumber(price_cents_str)
if not price_cents then
    redis.call("INCRBY", KEYS[1], ARGV[1])
    return {"metadata_missing"}
end

-- Redis Lua numbers are doubles, so `tonumber(price_cents_str) *
-- tonumber(ARGV[1])` can lose precision for large-but-valid BIGINT
-- prices. Keep the wire value exact by multiplying the decimal string
-- by the (small, validated) quantity one digit at a time.
local function multiply_decimal_string_by_int(decimal_str, multiplier)
    if multiplier == 0 then
        return "0"
    end

    local out = {}
    local carry = 0
    for i = #decimal_str, 1, -1 do
        local digit = string.byte(decimal_str, i) - 48
        if digit < 0 or digit > 9 then
            return nil
        end

        local product = (digit * multiplier) + carry
        out[#out + 1] = string.char((product % 10) + 48)
        carry = math.floor(product / 10)
    end

    while carry > 0 do
        out[#out + 1] = string.char((carry % 10) + 48)
        carry = math.floor(carry / 10)
    end

    for i = 1, math.floor(#out / 2) do
        local j = #out - i + 1
        out[i], out[j] = out[j], out[i]
    end

    return table.concat(out)
end

local quantity = tonumber(ARGV[1])
local amount_cents = multiply_decimal_string_by_int(price_cents_str, quantity)
if not amount_cents then
    redis.call("INCRBY", KEYS[1], ARGV[1])
    return {"metadata_missing"}
end

-- 4. Return metadata directly to the caller (no XADD).
-- Stage 5 application layer publishes to Kafka next.
return {"ok", event_id, amount_cents, currency}
