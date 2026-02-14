# Senior Golang Engineer Code Review

## 1. Architecture & DDD Compliance

### Strengths
- **Clean Structure**: The `internal/{domain,application,infrastructure}` layout is solid and follows standard Go Clean Architecture patterns.
- **Dependency Rule**: The `domain` package does not import from `application` or `infrastructure`, preserving the dependency rule.

### Critical Issues (Violations of Rules)
- **Domain Purity**: `internal/domain/order.go` includes `` `json:"..."` `` tags.
  - **Rule Violation**: "Domain... Must have zero external dependencies (no JSON tags if possible)".
  - **Recommendation**: Create separate DTOs (Data Transfer Objects) in the `application` or `api` layer for JSON serialization. Map Domain entities to these DTOs before returning response.

## 2. Design Patterns & Observability

### Critical Issues
- **Intrusive Tracing**: `OTEL` logic is embedded directly in `BookingService` and Repositories.
  - **Rule Violation**: "Do not call otel.Tracer().Start() inside business logic".
  - **Recommendation**: Implement the **Decorator Pattern**. Create `BookingServiceTracingDecorator` that wraps `BookingService` and handles spans.
- **Logger Injection**: Logger is a struct field (`s.logger.Infow`).
  - **Rule Violation**: "Logger should wrap and get by context.Context".
  - **Recommendation**: Implement a context-aware logger helper (e.g., `logger.FromCtx(ctx)`). This allows middleware to inject request-scoped loggers (with TraceIDs) transparently.

## 3. Database & Transaction Management

### Critical Issues
- **Lack of Atomicity**: `BookTicket` performs standard `DeductInventory` and `CreateOrder` calls sequentially without a transaction.
  - **Risk**: If `CreateOrder` fails, inventory is permanently lost (deduction committed, order not created).
  - **Rule Violation**: "manage transaction begin and commit in service layer".
  - **Recommendation**: Implement a **Unit of Work (UoW)** pattern. The Service should verify the start of a transaction, pass the transaction context to repositories, and commit/rollback.

## 4. General Best Practices

### configuration
- **Hardcoded Config**: `main.go` uses hardcoded default generic flags.
- **Recommendation**: Use strict environment variable parsing (e.g., `kelseyhightower/envconfig`) or a config loader. Twelve-Factor App compliance is recommended.

### Testing
- **Coverage**: Logic relies heavily on manual stress testing.
- **Recommendation**: Add table-driven unit tests for `BookingService`, mocking the repositories to test edge cases (e.g., repository errors, zero quantity).

## Action Plan (Summary)

1.  **Refactor Domain**: Remove JSON tags, create DTOs.
2.  **Refactor Observability**: Implement Decorators for Tracing.
3.  **Refactor Logging**: Implement Context-based Logger.
4.  **Refactor Persistence**: Implement Unit of Work for Transactions.
5.  **Testing**: Basic Unit Tests.

These items are already partially tracked in your `task.md`. I strongly suggest prioritizing the **Transaction/UoW** refactoring as it affects data integrity.
