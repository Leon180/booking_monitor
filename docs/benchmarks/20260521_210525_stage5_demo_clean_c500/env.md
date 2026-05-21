# Environment snapshot — clean re-run

Captured: 2026-05-21T13:05:25Z

## Pre-run state (verified clean)
- admin_event_bus_published_total{order.created}: 0
- admin_event_bus_channel_high_water_mark: 0
- PG orders row count: 0

## Host hardware
- macOS: 26.3 (25D125)
- CPU: Apple M5
- Cores: 10 physical, 10 logical
- Memory: 32.0 GiB
- Load avg: 1.17 1.85 2.31

## Docker Desktop
- Server Version: 29.3.1
- Kernel Version: 6.12.76-linuxkit
- Operating System: Docker Desktop
- Architecture: aarch64
- CPUs: 10
- Total Memory: 7.653GiB

## Stage 5 binary
- Branch: feat/stage5-admin-dashboard
- Commit: 4784b88e4df6bf11ab1c0cda4b179aef5f340b5e
- Source: cmd/booking-cli-stage5/ (BUILD_TARGET=./cmd/booking-cli-stage5)

## Key env vars from .env
- KAFKA_BROKERS=kafka:9092
- GOGC=400
- GOMEMLIMIT=256MiB
- OTEL_TRACES_SAMPLER_RATIO=0.01
- WORKER_STREAM_READ_COUNT=10
- WORKER_STREAM_BLOCK_TIMEOUT=2s
- WORKER_MAX_RETRIES=3
- WORKER_RETRY_BASE_DELAY=100ms
- WORKER_FAILURE_TIMEOUT=5s
- WORKER_PENDING_BLOCK_TIMEOUT=100ms
- WORKER_READ_ERROR_BACKOFF=1s
- REDIS_INVENTORY_TTL=720h
- REDIS_IDEMPOTENCY_TTL=24h
- REDIS_MAX_CONSECUTIVE_READ_ERRORS=30
- PAYMENT_PROVIDER=stripe

## Running services at capture
    SERVICE          IMAGE                                     STATUS
    alert_logger     mendhak/http-https-echo:34                Up 2 days (healthy)
    alertmanager     prom/alertmanager:v0.27.0                 Up 2 days
    app              booking_monitor-app                       Up 35 seconds (healthy)
    postgres         postgres:15-alpine                        Up 2 days
    expiry_sweeper   booking_monitor-expiry_sweeper            Up 2 days (healthy)
    grafana          grafana/grafana:11.2.2                    Up 2 days
    jaeger           jaegertracing/all-in-one:1.60             Up 2 days
    kafka            confluentinc/cp-kafka:7.6.0               Up 2 days (healthy)
    nginx            nginx:1.27-alpine                         Up About an hour
    prometheus       prom/prometheus:v2.54.1                   Up 2 days
    recon            booking_monitor-recon                     Up 2 days (healthy)
    redis            redis:7-alpine                            Up 2 days (healthy)
    redis_exporter   oliver006/redis_exporter:v1.83.0-alpine   Up 2 days (healthy)
    saga_watchdog    booking_monitor-saga_watchdog             Up 2 days (healthy)
    zookeeper        confluentinc/cp-zookeeper:7.6.0           Up 2 days (healthy)

## docker stats before benchmark
    NAME                     CPU %     MEM USAGE / LIMIT
    booking_nginx            0.00%     2.719MiB / 7.653GiB
    booking_app              0.02%     14.69MiB / 7.653GiB
    booking_grafana          0.04%     128.9MiB / 7.653GiB
    booking_prometheus       0.00%     102.6MiB / 7.653GiB
    booking_kafka            0.63%     894.3MiB / 7.653GiB
    booking_alertmanager     0.08%     32.33MiB / 7.653GiB
    booking_recon            0.00%     38.8MiB / 7.653GiB
    booking_redis_exporter   0.00%     23.94MiB / 7.653GiB
    booking_saga_watchdog    0.00%     32.17MiB / 7.653GiB
    booking_expiry_sweeper   0.00%     35.16MiB / 7.653GiB
    booking_jaeger           0.01%     105.2MiB / 7.653GiB
    booking_alert_logger     0.00%     35.48MiB / 7.653GiB
    booking_zookeeper        0.11%     169.4MiB / 7.653GiB
    booking_db               0.00%     179.1MiB / 7.653GiB
    booking_redis            0.63%     335.9MiB / 7.653GiB

## Small-scale validation (5 VU × 3s × pool 100)

Captured immediately after the canonical 60s run (with reset-db + app
restart in between). Same code path, just lower concurrency.

| Metric | Value |
|---|---|
| accepted_bookings | **100** |
| Redis qty after (`ticket_type_qty:019e4aad-...`) | **0** |
| Match? | **Yes — 100 - 100 = 0, perfect** |

This isolates the over-accept to a high-concurrency-only race. Lua
deduct logic is correct in steady-state; something only goes wrong
above some VU threshold during the 60s canonical run.
