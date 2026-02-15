# Booking Monitor System

A high-concurrency ticket booking system simulation, evolving from direct DB usage to advanced caching and queueing patterns.

## Architecture

This project follows **Domain-Driven Design (DDD)** and **Clean Architecture** principles.

- **cmd/booking-cli**: Main entry point (Cobra CLI).
- **internal/domain**: Core business entities (`Event`, `Order`) and repository interfaces.
- **internal/application**: Application services orchestration (`BookingService`).
- **internal/infrastructure**: Adapters for Database (Postgres), Redis, Http Handlers, Observability.
- **pkg**: Shared libraries (`logger`).
- **deploy**: Infrastructure configuration (Postgres, Prometheus, Grafana).

## Prerequisites

- Go 1.25+
- Docker & Docker Compose
- `golangci-lint` (for linting)
- `golang-migrate` (for database migrations)

## Features

- **Multi-Ticket Booking**: Support booking 1-10 tickets per request.
- **Observability**: Full stack monitoring with Prometheus, Grafana, and Jaeger.
- **Structured Logging**: High-performance logging using Uber Zap.
- **Database Migrations**: Versioned schema control.

## Quick Start

1. **Start Infrastructure**:
   ```bash
   docker-compose up -d
   ```

2. **Run Migrations**:
   ```bash
   make migrate-up
   ```

3. **Build CLI**:
   ```bash
   make build
   ```

4. **Run Server**:
   ```bash
   make run-server
   ```
   Server listens on port 8080.
   Metrics available at `http://localhost:8080/metrics`.

5. **Run Stress Test**:
   ```bash
   make run-stress
   # Or with custom parameters:
   make run-stress C=100 N=500
   ```

## Development Commands

- **Run Tests (Race Detector)**:
  ```bash
  make test
  ```

- **Lint Code**:
  ```bash
  make lint
  ```

- **Database Migrations**:
  ```bash
  make migrate-create name=add_users
  make migrate-up
  make migrate-down
  ```

## Observability

- **Unified Logger**: Uses `go.uber.org/zap` for structured JSON logging.
- **Tracing**: Integrated with OpenTelemetry (OTEL). Spans are created for API -> Service -> Repository layers.
- **Metrics**: Prometheus metrics exposed at `/metrics`.
- **Dashboards**: Grafana pre-provisioned at `http://localhost:3000` (admin/admin).

## Configuration

Database connection can be configured via flags:
```bash
./bin/booking-cli server --db "postgres://user:password@localhost:5433/booking?sslmode=disable"
```

## CLI Reference

### Server
Start the API server.
```bash
./bin/booking-cli server [flags]

Flags:
      --db string     Database connection string (default "postgres://user:password@localhost:5433/booking?sslmode=disable")
      --port string   Server port (default "8080")
  -h, --help          help for server
```

### Stress Test
Run a load test against the server.
```bash
./bin/booking-cli stress [flags]

Flags:
  -c, --concurrency int   Concurrency level (default 1000)
  -n, --requests int      Total requests (default 2000)
      --port string       Target server port (default "8080")
  -h, --help              help for stress
```
