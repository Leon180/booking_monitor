# Environment snapshot

Captured: 2026-05-21T12:48:00Z (2026-05-21 20:48:00 CST)

## Host hardware
- macOS: 26.3 (25D125)
- CPU model: Apple M5
- Physical cores: 10
- Logical cores: 10
- Memory: 32.0 GiB
- Load average at capture: 1.40 2.19 2.47

## Docker Desktop
- Server Version: 29.3.1
- Kernel Version: 6.12.76-linuxkit
- Operating System: Docker Desktop
- Architecture: aarch64
- CPUs: 10
- Total Memory: 7.653GiB

## Go toolchain (host)
- go version go1.25.10 darwin/arm64

## Stage 5 binary build
- Branch: feat/stage5-admin-dashboard
- Commit: ec3b09154d002e8b173624cfd1ef4b8064628e43
- Source: `cmd/booking-cli-stage5/` (BUILD_TARGET=./cmd/booking-cli-stage5)
- Docker image: sha256:862b1e54f0cd6

## Key env vars (from .env, secrets redacted)
- REDIS_ADDR=booking_redis:6379
- REDIS_PASSWORD=smoketest_redis_local
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
    app              booking_monitor-app                       Up 54 minutes (healthy)
    postgres         postgres:15-alpine                        Up 2 days
    expiry_sweeper   booking_monitor-expiry_sweeper            Up 2 days (healthy)
    grafana          grafana/grafana:11.2.2                    Up 2 days
    jaeger           jaegertracing/all-in-one:1.60             Up 2 days
    kafka            confluentinc/cp-kafka:7.6.0               Up 2 days (healthy)
    nginx            nginx:1.27-alpine                         Up 54 minutes
    prometheus       prom/prometheus:v2.54.1                   Up 2 days
    recon            booking_monitor-recon                     Up 2 days (healthy)
    redis            redis:7-alpine                            Up 2 days (healthy)
    redis_exporter   oliver006/redis_exporter:v1.83.0-alpine   Up 2 days (healthy)
    saga_watchdog    booking_monitor-saga_watchdog             Up 2 days (healthy)
    zookeeper        confluentinc/cp-zookeeper:7.6.0           Up 2 days (healthy)
