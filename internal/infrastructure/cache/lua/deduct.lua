-- KEYS[1]: inventory_key (event:{id}:qty)
-- ARGV[1]: count
-- ARGV[2]: event_id
-- ARGV[3]: user_id              (passed through to stream only)
-- ARGV[4]: order_id             (caller-minted UUIDv7; passed through to stream only)
-- ARGV[5]: reserved_until_unix  (D3 — Pattern A reservation TTL as
--                                UTC unix seconds; passed through
--                                to stream so the worker can write
--                                orders.reserved_until alongside the
--                                INSERT, no second Redis trip needed)
-- ARGV[6]: ticket_type_id       (D4.1 — KKTIX 票種; passed through to
--                                stream so the worker can write
--                                orders.ticket_type_id verbatim)
-- ARGV[7]: amount_cents         (D4.1 — price snapshot at book time;
--                                priceCents × quantity, computed in
--                                BookingService against the live
--                                ticket_type lookup. Passed as a
--                                decimal string (RESP encoding from
--                                go-redis: int64 → strconv.FormatInt
--                                → bulk string). The Lua script never
--                                calls tonumber() on it — ARGV[7]
--                                stays a string, gets handed to XADD
--                                verbatim, and the consumer parses
--                                with strconv.ParseInt. No floating-
--                                point arithmetic involved on either
--                                side, so the IEEE-754 exact-int range
--                                is irrelevant; the field round-trips
--                                bit-exact for any int64.)
-- ARGV[8]: currency             (D4.1 — 3-letter lowercase ISO 4217;
--                                already normalised by BookingService
--                                via NewReservation invariants)

-- 1. Deduct inventory atomically
local new_val = tonumber(redis.call("DECRBY", KEYS[1], ARGV[1]))

-- 2. If inventory went negative, revert and return sold out
if new_val < 0 then
    redis.call("INCRBY", KEYS[1], ARGV[1])
    return -1 -- Sold Out
end

-- 3. Publish to Stream — order_id flows end-to-end so the worker can
--    use the caller's id verbatim instead of generating a fresh one
--    on every PEL retry. reserved_until + ticket_type_id + amount_cents
--    + currency are included as part of the same atomic XADD so a
--    Pattern A reservation is fully described by one stream message
--    (Lua → worker → DB).
redis.call("XADD", "orders:stream", "*",
    "order_id",       ARGV[4],
    "user_id",        ARGV[3],
    "event_id",       ARGV[2],
    "quantity",       ARGV[1],
    "reserved_until", ARGV[5],
    "ticket_type_id", ARGV[6],
    "amount_cents",   ARGV[7],
    "currency",       ARGV[8])

return 1 -- Success
