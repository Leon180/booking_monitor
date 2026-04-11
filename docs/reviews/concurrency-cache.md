# Review: Concurrency & Redis/Lua Hot Path

**Branch:** `review/concurrency-cache` **Agent:** `go-reviewer` **Date:** 2026-04-11
**Scope:** `internal/infrastructure/cache/**`, `internal/domain/lock.go`, `internal/domain/queue.go`, lua scripts

---

## Summary

Static analysis (`go vet`, `staticcheck`) passes clean. Five substantive issues were found: two HIGH (unsafe error discard in `Sscanf`/`XAck` chains, `time.Sleep` inside retry loop ignores ctx cancellation), two MEDIUM (revert.lua TTL race on idempotency key, `processPending` uses `Block:0` which can stall startup context), and one LOW (NOGROUP string-match fragility). No CRITICAL issues found. The pgAdvisoryLock recursion fix (commit f56ab82) is correctly implemented and verified. The deduct.lua atomicity is correct. Revert.lua is idempotent under normal conditions with a documented TTL caveat.

---

## Findings

### HIGH-1 — Discarded `Sscanf` errors silently produce zero-value IDs

**File:** [`redis_queue.go:191-193`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go)

```go
_, _ = fmt.Sscanf(userIDStr, "%d", &userID)
_, _ = fmt.Sscanf(eventIDStr, "%d", &eventID)
_, _ = fmt.Sscanf(qtyStr, "%d", &qty)
```

All three parse errors are discarded via `_, _`. If a field is present but non-numeric (e.g. `"abc"`), `Sscanf` writes `0` into the variable and returns an error — the message is considered structurally valid (non-empty string asserts pass in `parseMessage`) and is passed to the handler with `userID=0`, `eventID=0`, `qty=0`. This silently creates orders for event 0 / user 0 and debits inventory for event 0, which may not exist. Use `strconv.Atoi` and propagate the error:

```go
userID, err := strconv.Atoi(userIDStr)
if err != nil {
    return nil, fmt.Errorf("invalid user_id %q: %w", userIDStr, err)
}
```

---

### HIGH-2 — `time.Sleep` in retry loop ignores context cancellation

**File:** [`redis_queue.go:132`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go)

```go
time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
```

If the calling context is cancelled (graceful shutdown), `processWithRetry` will still block for up to 300 ms on its final sleep before returning. For a single message this is minor, but under 3 retries × 10 messages per batch, this can delay shutdown by ~9 seconds. Replace with a context-aware sleep:

```go
select {
case <-time.After(time.Duration(i+1) * 100 * time.Millisecond):
case <-ctx.Done():
    return ctx.Err()
}
```

---

### HIGH-3 — All `XAck` and `XAdd` (DLQ) return values silently dropped

**Files:** [`redis_queue.go:105`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go), [`redis_queue.go:115`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go), [`redis_queue.go:150`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go), [`redis_queue.go:164`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go), [`redis_queue.go:234`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go), [`redis_queue.go:243`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go)

Every call to `q.client.XAck(...)` and `q.client.XAdd(...)` (moveToDLQ) discards the returned `*redis.IntCmd` / `*redis.StringCmd` without calling `.Err()`. A failed ACK means the message re-enters the PEL and will be reprocessed — silent data duplication risk. A failed DLQ write means the message is lost without trace.

```go
// current (silent):
q.client.XAck(ctx, streamKey, groupName, msg.ID)

// required:
if err := q.client.XAck(ctx, streamKey, groupName, msg.ID).Err(); err != nil {
    q.logger.Errorw("failed to ACK message", "msg_id", msg.ID, "error", err)
}
```

---

### MEDIUM-1 — revert.lua idempotency key TTL race can allow double-compensation

**File:** [`lua/revert.lua:5-7`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/lua/revert.lua)

```lua
if redis.call("SETNX", KEYS[2], "1") == 1 then
    redis.call("EXPIRE", KEYS[2], 604800)
    ...
```

`SETNX` + `EXPIRE` are two separate commands inside the same Lua script, so they are atomic in Redis single-threaded execution — this is safe against concurrent callers. However, if the Redis instance crashes between `SETNX` and `EXPIRE` (or if the script is interrupted by `SCRIPT KILL`), the idempotency key will persist without a TTL, effectively making it permanent and blocking future legitimate compensations (edge case only under crash scenarios).

Replace with `SET key "1" EX 604800 NX` (single atomic command):

```lua
if redis.call("SET", KEYS[2], "1", "EX", 604800, "NX") then
    ...
```

This is the standard Redis idempotent-set pattern and eliminates the persistent-key risk.

---

### MEDIUM-2 — `processPending` with `Block:0` can stall startup indefinitely if Redis is slow

**File:** [`redis_queue.go:214`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go)

```go
Block: 0,  // "0" = Pending messages
```

`Block:0` means "block forever". Although this is called during startup (before the main loop) and the passed `ctx` will eventually cancel it, there is no dedicated timeout on the pending-recovery phase. A Redis connection stall here blocks `Subscribe` entirely until the parent context times out (which may be the application shutdown context — many minutes). Consider using a bounded timeout context for the pending-recovery phase:

```go
pelCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
if err := q.processPending(pelCtx, consumerName, handler); err != nil { ... }
```

---

### LOW-1 — NOGROUP error matched by raw string comparison

**File:** [`redis_queue.go:82`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis_queue.go)

```go
if err.Error() == "NOGROUP No such key 'orders:stream' ..."
```

and similarly in `EnsureGroup`:

```go
if err.Error() == "BUSYGROUP Consumer Group name already exists"
```

Redis error messages are not guaranteed stable across versions. A minor Redis upgrade or proxy layer (e.g. Twemproxy, Envoy) can change the prefix. Use `strings.Contains(err.Error(), "BUSYGROUP")` / `strings.Contains(err.Error(), "NOGROUP")` as a minimum, or use the `rueidis` or typed-error approach if migrating client libraries.

---

### NIT-1 — `SetInventory` uses no TTL; inventory key never expires

**File:** [`redis.go:80`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/redis.go)

```go
return r.client.Set(ctx, inventoryKey(eventID), count, 0).Err()
```

TTL of `0` means no expiry. For events that end, the key stays in Redis indefinitely. This is a memory management concern, not a correctness concern for active events. Consider setting an event-lifetime TTL (e.g. 7 days) if events have a defined end date, or documenting the intentional lack of TTL.

---

### NIT-2 — `pgAdvisoryLock` recursion fix verified (no issue)

**File:** [`advisory_lock.go:24-53`](/Users/lileon/project/booking_monitor/internal/infrastructure/persistence/postgres/advisory_lock.go)

Commit f56ab82's claim is correct. The prior recursion was that a caller holding the lock would call `TryLock` again and issue a redundant DB query. The fix short-circuits via `if l.conn != nil { return true, nil }` under `l.mu.Lock()`. The connection is nilled before returning `false` on failure. `Unlock` also holds `l.mu` and nils `l.conn` before returning. No recursive path remains. The fix is correct.

---

### NIT-3 — `deduct.lua` atomicity is correct (no issue)

**File:** [`lua/deduct.lua`](/Users/lileon/project/booking_monitor/internal/infrastructure/cache/lua/deduct.lua)

The script correctly: (1) `DECRBY` atomically, (2) checks `< 0` (not `<= 0`, allowing zero-ticket remainder — correct for exact depletion), (3) `INCRBY` to restore on oversell, (4) `XADD` to stream. KEYS/ARGV counts match the Go caller at `redis.go:84-86`. The Lua execution is single-threaded in Redis so no interleaving is possible. No issues.

---

## Test-Coverage Gaps

| Gap | Risk |
|-----|------|
| No test for `Sscanf` parse failure with malformed-but-present field (e.g. `user_id="abc"`) producing `ID=0` orders | HIGH — silent data corruption path untested |
| No test for `XAck` failure (Redis returns error mid-ACK) | HIGH — silent re-delivery not detectable |
| No test for `processWithRetry` respecting ctx cancellation during sleep | MEDIUM — shutdown delay not caught |
| No test for `revert.lua` with crash between SETNX and EXPIRE (requires miniredis kill injection) | MEDIUM — edge case, but saga correctness |
| `processPending` with `Block:0` has no test covering a slow/hung Redis at startup | LOW |
| `SetInventory` TTL=0 behaviour not tested for memory leak over event lifecycle | NIT |

---

## Follow-Up Questions

1. **Cluster mode**: Is Redis operated as a single node or cluster? `deduct.lua` uses a hardcoded stream key `orders:stream` not in `KEYS[]`, which violates Redis Cluster's single-slot rule. If cluster is ever adopted, the stream key must be added to `KEYS` or use hash tags.

2. **XCLAIM / stale PEL**: `processPending` only claims messages owned by `consumerName`. If a different consumer crashes, its PEL entries are never reclaimed. Is there a separate XCLAIM sweep for cross-consumer recovery?

3. **DLQ consumer**: The `orders:dlq` stream is written to but there is no consumer defined in scope. Is there a separate DLQ worker planned (roadmap mentions DLQ Worker)?

4. **`revert.lua` inventory floor**: `INCRBY` can restore inventory above the original maximum if called multiple times (e.g. compensation fires twice with different `compensationID`s for the same booking). Should `revert.lua` cap at the initial max value?

5. **`handleFailure` compensation ordering**: Inventory is reverted before the message is ACKed. If `RevertInventory` succeeds but `XAck` fails (swallowed error, see HIGH-3), the message re-enters the PEL and `handleFailure` fires again — `revert.lua` deduplicates on `rawMsg.ID`, but a second DLQ entry is written for the same failure. Is duplicate DLQ acceptable?

---

## Out of Scope / Deferred

- Kafka consumer and saga compensator goroutine behaviour — covered by dimension #1 (domain-application)
- PostgreSQL advisory lock implementation detail beyond recursion fix — covered by dimension #3 (persistence)
- API rate limiter and idempotency header logic — covered by dimension #1
- `redisIdempotencyRepository` (`idempotency.go`): straightforward `SET`/`GET` with TTL, no concurrency primitives; no findings
