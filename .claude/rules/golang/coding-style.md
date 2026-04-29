---
paths:
  - "**/*.go"
  - "**/go.mod"
  - "**/go.sum"
---
# Go Coding Style

> This file extends [common/coding-style.md](../common/coding-style.md) with Go specific content.

## Formatting

- **gofmt** and **goimports** are mandatory — no style debates

## Design Principles

- Accept interfaces, return structs
- Keep interfaces small (1-3 methods)

## Error Handling

Always wrap errors with context:

```go
if err != nil {
    return fmt.Errorf("failed to create user: %w", err)
}
```

## Domain Entities

Entities live under `internal/domain/`. Per the project's immutability
rule (every layer, but most strictly here), the conventions are:

1. **Construct via factory, not struct literal.** New entities go
   through `NewX(...)` so invariant validation lives in one place.
   `&domain.Order{...}` at a call site is a smell — it bypasses the
   factory. Validation errors are exported sentinels (`ErrInvalidUserID`,
   `ErrInvalidQuantity`, etc.) so callers can branch on them with
   `errors.Is`.

2. **Factories return values, not pointers.** `func NewOrder(...) (Order, error)`
   over `func NewOrder(...) (*Order, error)`. Pointer-write-back from
   repos (`Create(ctx, *Order) error` mutating `order.ID`) is a stdlib-
   legacy pattern (`sql.Row.Scan(&x)`); application code prefers
   value semantics so the function signature is honest about what
   gets mutated.

3. **Rehydration uses `ReconstructX`**, not `NewX`. Repo row-scan code
   calls `ReconstructOrder(id, userID, ..., status, createdAt)` —
   bypasses invariant validation because persisted state was already
   validated at create-time. **Don't** use `Reconstruct` from
   application code; it exists only for the persistence boundary.

4. **State transitions return new values**, never mutate the receiver:
   ```go
   func (o Order) WithStatus(s OrderStatus) Order {
       o.Status = s
       return o          // o is a value receiver; the original is untouched
   }
   ```

5. **Wire-contract strings live as typed constants in domain.** Event
   type names (`EventTypeOrderCreated`, `EventTypeOrderFailed`),
   statuses (`OutboxStatusPending`), and other cross-process strings
   never appear as inline literals at call sites. Each named
   string should also have a paired `New*Outbox` / similar factory
   so callers don't even need to spell the constant.

6. **Tests for new entity factories are mandatory** — at minimum,
   one positive case + one case per invariant violation + one
   immutability-of-receiver test for any `WithX` method.

7. **Domain entity fields are unexported.** Reads via accessor
   methods (Wild Workouts pattern: `Status()` not `GetStatus()`,
   no `Get` prefix — aligned with Go stdlib like `time.Time.Hour()`).
   Writes via factories or immutable `WithX` transitions. Boundary
   layers handle their own JSON / SQL serialisation (DTOs in
   `api/dto/`, persistence rows in `postgres/*_row.go`); the
   domain itself never carries `json:` or `db:` tags. PR 34
   established this pattern across all three aggregates (Order,
   Event, OutboxEvent).

8. **IDs are caller-generated and validated by the factory; never
   DB-assigned, never ORM-hook-assigned.** Use `uuid.NewV7()` (RFC
   9562) at the call site that owns identity (typically the API
   boundary or another aggregate factory) and pass it into `NewX()`,
   which validates `id != uuid.Nil` along with the rest of the
   invariants. The aggregate is fully complete the moment the
   factory returns — no repository "fills in" anything. Repos pass
   IDs through verbatim; INSERT statements write all
   factory-assigned values explicitly (no `RETURNING id`).
   `CreatedAt` follows a related rule: factory-assigned via
   `time.Now()`, not DB-assigned.

   **Why caller-generated, not factory-internal**: the same id has
   to flow end-to-end across async boundaries — the API handler
   returns it to the client, the queue carries it to the worker,
   the worker passes it to the repo, the outbox event references
   it, saga / payment / reconciler operate on it. If the worker
   re-mints inside `NewX`, PEL retries produce a fresh id per
   delivery and diverge from what the client already has in hand
   (PR #47 fixed this for `domain.Order`).

   Rationale: aggregate identity is a domain concern, not a
   persistence concern AND not a worker concern — it belongs at
   the call site that first observes the entity (Vaughn Vernon,
   IDDD).

## Reference

See skill: `golang-patterns` for comprehensive Go idioms and patterns.
See [patterns.md](patterns.md) for the entity-factory code shape.
