---
trigger: always_on
---

# Project Rules & Guidelines

## 1. Architecture Standards
- **Clean Architecture & DDD**:
  - **Domain (`internal/domain`)**: Pure business logic and entities. Must have **zero** external dependencies (no JSON tags if possible, definitely no tracing/logging implementations).
  - **Application (`internal/application`)**: Service layer defining use cases. Orchestrates Domain entities.
  - **Infrastructure (`internal/infrastructure`)**: Implements interfaces (Repositories, APIs).
  - **Shared (`pkg`)**: Agnostic utilities.

## 2. Design Patterns & Observability (OTEL)
- **Zero-Intrusion Tracing**:
  - **Do not** call `otel.Tracer().Start()` inside business logic (Service/Domain) methods.
  - **Use Decorators**: Implement tracing using the **Decorator Pattern**. Wrap services and repositories with a tracing struct that implements the same interface.
  - **Recommended Flow**:
    ```text
    HTTP Request
        ↓
    [Tracing Middleware] ← Start Root Span
        ↓
    [Service Decorator]  ← Start Child Span (Transparent to Logic)
        ↓
    [Business Logic]     ← Receives Context only
        ↓
    [Repo Decorator]     ← Start Child Span (Transparent to Logic)
        ↓
    [Database Driver]    ← Automatic DB Spans (via instrumented driver)
    ```

## 3. Transaction Management
- **Service Layer Responsibility**:
  - Database transactions (`Begin`, `Commit`, `Rollback`) must be managed in the **Application Service Layer**.
  - Repositories should not initiate transactions but should participate in the current transaction context (e.g., passing `tx` via `context` or a Unit of Work abstraction).
- **Entity Lifecycle Separation**:
  - Operations should follow the flow: **Fetch** (Repo) -> **Domain Logic** (Entity Method) -> **Persist** (Repo with TX).
  - Database interactions should be minimized during the Domain Logic phase. Persistence should happen at the end of the transaction.

## 4. Logging Strategy
- **Context Injection**:
  - The Logger should be initialized at server start and injected into `context.Context` via Middleware.
  - Components should retrieve the specific logger (potentially with request-scoped fields like TraceID) from the `context`.
  - Helper: `logger.FromCtx(ctx).Info(...)`.

## 5. Dependency Injection
- **Uber Fx**: Use `go.uber.org/fx` for dependency injection.
  - **Modules**: Organize providers into Fx modules for better modularity.
  - **Lifecycle**: Use `fx.Lifecycle` for managing start/stop of components (like DB connections, Servers).

## 6. Security
- always check and hide security info before commit

## 7. Testing Standards
- **Unit Tests**:
  - Required for all **Domain Entities** and **Application Services**.
  - **Mocks**: Use mocks for external dependencies (Repositories, UoW) to test business logic in isolation.
  - **Coverage**: Aim for high coverage (>80%) in `internal/domain` and `internal/application`.
- **Table-Driven Tests**: Use table-driven tests for multiple scenarios (happy path, edge cases, errors).
- **Parallel Execution**: Use `t.Parallel()` where safe.
