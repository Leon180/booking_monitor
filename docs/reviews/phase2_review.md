# Code Review Summary: Phase 2 (Redis Integration)

## Files Reviewed
- `internal/domain/inventory.go` (New)
- `internal/infrastructure/cache/redis.go` (New)
- `internal/application/booking_service.go`
- `internal/application/event_service.go`
- `internal/application/booking_service_test.go`
- `cmd/booking-cli/main.go`
- `docker-compose.yml`
- `Makefile`

## ✅ Approved
- **Architecture**: Strictly follows the project's DDD/Clean Architecture.
    - `InventoryRepository` interface is correctly defined in the `domain` layer.
    - `redisInventoryRepository` implementation is isolated in `infrastructure`.
    - Dependencies are injected via `fx` modules.
- **Correctness**: The `BookingService` logic correctly implements the "Redis-First" flow for Phase 2, prioritizing throughput.
- **Testing**: Unit tests in `booking_service_test.go` were properly updated to mock the new `InventoryRepository` and validate the Redis-specific flows (Success, Sold Out, Error).
- **Configuration**: Redis client is configured with appropriate timeouts (`2s`) and pool size (`100`) for high-concurrency environments.
- **Infrastructure**: `docker-compose.yml` correctly adds the Redis service with a health check.

## ⚠️ Suggestions
- **Atomicity (Race Condition)**:
    - In `redis.go`, `DeductInventory` uses `DECRBY` followed by a compensatory `INCRBY` (rollback) if the result is negative.
    - **Risk**: If the application crashes between the decrement and the rollback, the inventory count will be permanently inconsistent (lower than actual).
    - **Remediation**: Use a **Lua script** to blindly check-and-decrement atomically.
    ```lua
    if redis.call("GET", KEYS[1]) >= ARGV[1] then
        return redis.call("DECRBY", KEYS[1], ARGV[1])
    else
        return -1
    end
    ```
- **Data Persistence (Phase 2 constraint)**:
    - `BookingService` returns success without creating an `Order` in Postgres.
    - **Risk**: If Redis crashes, all bookings since the last snapshot are lost. This is a known constraint for Phase 2 but must be addressed immediately in Phase 3 (Kafka).
- **Security**:
    - Redis is configured without a password. This is acceptable for this local benchmark environment but should be secured for any shared/production deployment.

## ❌ Issues to Fix
- None. The changes meet the requirements for Phase 2.
