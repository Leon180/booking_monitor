PROJECT_NAME := booking-cli
BUILD_DIR := bin
CMD_PATH := ./cmd/booking-cli

# Load secrets / connection strings from .env if present (non-fatal if missing).
# `export` makes every variable visible to child processes (docker exec, migrate, etc.).
-include .env
export

.PHONY: all build clean test lint modernize run-server run-stress deps help

all: build

## parameters:
##   -race: enable race detection

deps: ## Download dependencies
	go mod tidy
	go mod download

build: deps ## Build the binary
	@mkdir -p $(BUILD_DIR)
	go build -race -o $(BUILD_DIR)/$(PROJECT_NAME) $(CMD_PATH)

clean: ## Clean build directory
	rm -rf $(BUILD_DIR)

test: ## Run tests with race detection
	go test -race -v ./internal/...

test-integration: ## Run testcontainers-backed integration tests (requires Docker; CP4)
	go test -tags=integration -race -v -timeout 5m ./test/integration/...

test-all: test test-integration ## Run unit + integration tests sequentially

lint: ## Run linter
	@which golangci-lint > /dev/null || echo "Please install golangci-lint"
	golangci-lint run ./...

modernize: ## Apply modern patterns (formatting and vetting for now)
	go fmt ./...
	go vet ./...

run-server: build ## Run the API server
	./$(BUILD_DIR)/$(PROJECT_NAME) server

C ?= 1000
N ?= 2000
EVENTS ?= 5
TICKETS ?= 1000

run-stress: build ## Run the stress test (usage: make run-stress C=100 N=5000)
	./$(BUILD_DIR)/$(PROJECT_NAME) stress -c $(C) -n $(N)

stress: ## Run stress test without rebuilding
	./$(BUILD_DIR)/$(PROJECT_NAME) stress -c $(C) -n $(N)

stress-go: ## Run advanced stress test with multiple events (usage: make stress-go C=50 N=1000 EVENTS=5 TICKETS=500)
	go run scripts/stress_load.go -c $(C) -n $(N) -events $(EVENTS) -tickets $(TICKETS)

VUS ?= 500
DURATION ?= 30s

stress-k6: ## Run K6 load test via Docker (usage: make stress-k6 VUS=1000 DURATION=60s)
	docker run --rm -i --network=booking_monitor_default -e VUS=$(VUS) -e DURATION=$(DURATION) -v $(PWD)/scripts/k6_load.js:/script.js grafana/k6 run /script.js

# D9-minimal — k6 two-step (book → /pay → confirm → poll-paid OR abandon → expire → compensated)
TWO_STEP_VUS ?= 100
TWO_STEP_DURATION ?= 90s
TWO_STEP_ABANDON_RATIO ?= 0.2
TWO_STEP_TICKET_POOL ?= 50000
bench-two-step: ## D9 — k6 two-step flow benchmark (usage: make bench-two-step TWO_STEP_VUS=100 TWO_STEP_DURATION=90s)
	@echo "Pre-flight: stack must be up via 'make demo-up' (BOOKING_RESERVATION_WINDOW=20s + EXPIRY_SWEEP_INTERVAL=5s)"
	@docker run --rm -i --network=booking_monitor_default \
	  -e API_ORIGIN=http://app:8080 \
	  -e VUS=$(TWO_STEP_VUS) \
	  -e DURATION=$(TWO_STEP_DURATION) \
	  -e ABANDON_RATIO=$(TWO_STEP_ABANDON_RATIO) \
	  -e TICKET_POOL=$(TWO_STEP_TICKET_POOL) \
	  -v $(PWD)/scripts/k6_two_step_flow.js:/script.js \
	  grafana/k6 run /script.js

# D10-minimal — bring up stack with demo-friendly env (CORS + test endpoints +
# 20s reservation + 5s sweep). Recipe lines don't share shell state, so env
# is set inline on the docker compose command (NOT via separate `export`).
# `--force-recreate` is mandatory when changing BOOKING_RESERVATION_WINDOW
# because docker compose reuses existing containers with the OLD env value.
demo-up: ## D10 — bring up the demo stack (CORS + test endpoints + 20s reservation + 5s sweep)
	@echo "Starting demo stack with: APP_ENV=development, ENABLE_TEST_ENDPOINTS, BOOKING_RESERVATION_WINDOW=20s, EXPIRY_SWEEP_INTERVAL=5s"
	@# nginx is included so http://localhost:80 (the host-published surface
	@# the walkthrough + scripts target by default) is actually reachable —
	@# the `app` service publishes pprof:6060 only, NOT 8080.
	@APP_ENV=development \
	  CORS_ALLOWED_ORIGINS=http://localhost:5173,http://127.0.0.1:5173 \
	  ENABLE_TEST_ENDPOINTS=true \
	  PAYMENT_WEBHOOK_SECRET=demo_secret_local_only \
	  BOOKING_RESERVATION_WINDOW=20s \
	  EXPIRY_SWEEP_INTERVAL=5s \
	  docker compose up -d --build --force-recreate app expiry_sweeper nginx
	@echo ""
	@echo "Waiting for stack to be ready (livez + readyz via nginx)..."
	@for i in $$(seq 1 60); do \
	  if curl -sSf http://localhost/livez >/dev/null 2>&1 \
	      && curl -sSf http://localhost/readyz >/dev/null 2>&1; then \
	    echo "Stack ready at http://localhost"; \
	    break; \
	  fi; \
	  if [ $$i = 60 ]; then \
	    echo "ERROR: stack didn't pass /livez + /readyz within 60s"; \
	    exit 1; \
	  fi; \
	  sleep 1; \
	done
	@echo ""
	@echo "Try:"
	@echo "  scripts/d10_demo_walkthrough.sh             # terminal walkthrough"
	@echo "  asciinema rec docs/demo/walkthrough.cast \\"
	@echo "    --command='scripts/d10_demo_walkthrough.sh'  # record"
	@echo "  make bench-two-step                          # k6 D9 baseline"

benchmark: ## Run full benchmark with recording (usage: make benchmark VUS=1000 DURATION=60s)
	@chmod +x scripts/benchmark_k6.sh
	@./scripts/benchmark_k6.sh $(VUS) $(DURATION)

benchmark-compare: ## Run two-run comparison benchmark (usage: make benchmark-compare VUS=500 DURATION=60s)
	@chmod +x scripts/benchmark_compare.sh
	@./scripts/benchmark_compare.sh $(VUS) $(DURATION)

benchmark-redis-baseline: ## Compare deduct.lua vs raw Redis SET/GET/XADD/EVAL baselines (usage: make benchmark-redis-baseline REQS=100000 CLIENTS=50)
	@chmod +x scripts/redis_baseline_benchmark.sh
	@./scripts/redis_baseline_benchmark.sh $(REQS) $(CLIENTS)

profile-saturation: ## Run one-shot saturation diagnostic (usage: make profile-saturation VUS=500 DURATION=60s)
	@chmod +x scripts/profile_saturation.sh
	@# Override the global DURATION ?= 30s for this target only.
	@# Saturation profiling needs at least one full Prometheus rate
	@# window (60s) to see steady-state load — 30s only captures
	@# warmup-time rates. Use $$(origin) to detect whether DURATION
	@# came from the CLI/env (respect it) vs the global default
	@# (override to 60s).
	@VUS="$(VUS)" \
	DURATION="$(if $(filter command\ line environment,$(origin DURATION)),$(DURATION),60s)" \
	./scripts/profile_saturation.sh

docker-restart: ## Restart the API server in Docker (Rebuild and Up)
	@echo "Restarting booking_app container..."
	@docker-compose stop app
	@docker-compose build app
	@docker-compose up -d app
	@echo "App restarted in Docker."

# D12.5 — 4-stage comparison harness control surface. The compose file
# `docker-compose.comparison.yml` brings up bench-isolated Postgres +
# Redis + Kafka + 4 stage binaries on ports 8091-8094. Used by the
# orchestration script `scripts/run_4stage_comparison.sh` (Slice 3).
bench-up: ## D12.5 — bring up the 4-stage comparison harness (ports 8091-8094)
	@echo "[1/4] Starting backing services (postgres + redis + kafka + prometheus)..."
	@# Stage binaries hard-fail at startup if their DB has no tables
	@# (Stage 4's inventoryRehydrate fx hook queries `events`; Stages
	@# 1-3's sweepers query `orders`). So migrations MUST land before
	@# the stage binaries start. Bring up backing services first.
	@docker compose -f docker-compose.comparison.yml up -d --build \
	  postgres-bench redis-bench zookeeper-bench kafka-bench prometheus-bench
	@echo ""
	@echo "[2/4] Waiting for postgres-bench healthy..."
	@for i in $$(seq 1 30); do \
	  if [ "$$(docker inspect -f '{{.State.Health.Status}}' booking_bench_pg 2>/dev/null)" = "healthy" ]; then \
	    echo "  postgres-bench healthy"; break; \
	  fi; \
	  sleep 1; \
	done
	@echo ""
	@echo "[3/4] Applying migrations to all 4 stage DBs..."
	@for stage in 1 2 3 4; do \
	  echo "  migrating booking_stage$$stage..."; \
	  $(MAKE) -s migrate-up MIGRATE_DB_URL="postgres://booking:bench_pg_local@localhost:5434/booking_stage$$stage?sslmode=disable" 2>&1 | tail -3 || { echo "  ✗ migration failed for booking_stage$$stage"; exit 1; }; \
	done
	@echo ""
	@echo "[4/4] Starting stage binaries..."
	@docker compose -f docker-compose.comparison.yml up -d --build \
	  booking-cli-stage1 booking-cli-stage2 booking-cli-stage3 booking-cli-stage4
	@echo ""
	@echo "Waiting for all 4 stages to pass /livez (60s budget)..."
	@# Use /livez (not /readyz): stages 1-3 only register /livez (no
	@# ops package), so /readyz returns 404 from those binaries.
	@for stage in 1 2 3 4; do \
	  port=$$((8090 + $$stage)); \
	  ready=false; \
	  for i in $$(seq 1 60); do \
	    if curl -sSf "http://localhost:$${port}/livez" >/dev/null 2>&1; then \
	      echo "  stage $$stage on :$${port} live"; \
	      ready=true; break; \
	    fi; \
	    sleep 1; \
	  done; \
	  if [ "$${ready}" != "true" ]; then \
	    echo "  ✗ stage $$stage on :$${port} did NOT pass /livez within 60s"; \
	    docker compose -f docker-compose.comparison.yml logs --tail=30 booking-cli-stage$$stage; \
	    exit 1; \
	  fi; \
	done
	@echo ""
	@echo "All 4 stages ready:"
	@echo "  stage 1: http://localhost:8091  (sync SELECT FOR UPDATE)"
	@echo "  stage 2: http://localhost:8092  (Redis Lua + sync PG INSERT)"
	@echo "  stage 3: http://localhost:8093  (Redis Lua + async stream/worker)"
	@echo "  stage 4: http://localhost:8094  (Pattern A + saga compensator)"
	@echo "  bench Prometheus: http://localhost:9091"
	@echo ""
	@echo "Next: scripts/run_4stage_comparison.sh (Slice 3)"

bench-down: ## D12.5 — stop the 4-stage harness (keeps volumes for re-run)
	@docker compose -f docker-compose.comparison.yml down

bench-down-clean: ## D12.5 — stop AND remove volumes (resets all state)
	@docker compose -f docker-compose.comparison.yml down -v

bench-smoke: ## D12.5 — minimal smoke (VUS=1 DURATION=10s) to detect harness rot in CI
	@$(MAKE) bench-up
	@echo "Running 10s smoke against each stage (event create + k6 sanity for both scenarios)..."
	@# Closes Slice 9 review SFH M4: bench-smoke originally did just
	@# event-create curl (no k6 at all), so a broken k6 script (wrong
	@# threshold name, wrong metric ref, JS syntax error) wouldn't
	@# surface in CI. Now invokes k6 at VUS=1 / DURATION=5s against
	@# both scenarios per stage to actually exercise the script paths.
	@for stage in 1 2 3 4; do \
	  port=$$((8090 + $$stage)); \
	  echo "stage $$stage smoke (port $${port})..."; \
	  curl -sSf -X POST "http://localhost:$${port}/api/v1/events" \
	    -H 'Content-Type: application/json' \
	    -d '{"name":"smoke","total_tickets":10,"price_cents":1000,"currency":"usd"}' \
	    >/dev/null || { echo "  ✗ stage $$stage event create failed"; exit 1; }; \
	  echo "  ✓ stage $$stage event create OK"; \
	  echo "  → stage $$stage k6 intake-only smoke (VUS=1 DURATION=5s)..."; \
	  k6 run -q --vus 1 --duration 5s \
	    -e API_ORIGIN="http://localhost:$${port}" \
	    -e VUS=1 -e DURATION=5s -e TICKET_POOL=10000 \
	    scripts/k6_intake_only.js >/dev/null \
	    || { echo "  ✗ stage $$stage k6 intake-only smoke FAILED"; exit 1; }; \
	  echo "  ✓ stage $$stage k6 intake-only smoke OK"; \
	done
	@$(MAKE) bench-down-clean
	@echo "bench-smoke OK"

PAGE ?= 1
SIZE ?= 10
STATUS ?=

curl-history: ## Get booking history (usage: make curl-history PAGE=1 SIZE=5 STATUS=confirmed)
	@url="http://localhost:8080/api/v1/history?page=$(PAGE)&size=$(SIZE)"; \
	if [ -n "$(STATUS)" ]; then url="$$url&status=$(STATUS)"; fi; \
	curl "$$url" | jq

curl-history-input: ## Get booking history with interactive prompts
	@read -p "Page [1]: " p; page=$${p:-1}; \
	read -p "Size [10]: " s; size=$${s:-10}; \
	read -p "Status (optional): " st; \
	url="http://localhost:8080/api/v1/history?page=$$page&size=$$size"; \
	if [ -n "$$st" ]; then url="$$url&status=$$st"; fi; \
	echo "Fetching: $$url"; \
	curl "$$url" | jq


MOCKGEN := $(shell pwd)/bin/mockgen

tools: ## Install development tools (mockgen wrapper)
	@mkdir -p bin
	@echo "Creating mockgen wrapper..."
	@echo '#!/bin/sh' > bin/mockgen
	@echo 'go run go.uber.org/mock/mockgen@v0.6.0 "$$@"' >> bin/mockgen
	@chmod +x bin/mockgen

mocks: tools ## Generate mocks using local wrapper
	@echo "Generating mocks..."
	@PATH=$(shell pwd)/bin:$(PATH) go generate ./...

reset-db: ## Reset database — truncate orders/events/outbox + clear Redis cache keys (preserves stream + consumer group). Tests create their own events via POST /api/v1/events
	@echo "Resetting database..."
	# Post-PR-34 events.id is UUID, so the previous "seed event id=1"
	# pattern is gone. Truncate everything; each test run creates a
	# fresh event via the API.
	# orders + order_status_history truncated together — FK with
	# ON DELETE CASCADE rules out plain `TRUNCATE orders` (Postgres
	# rejects without CASCADE when child tables reference the parent).
	# RESTART IDENTITY resets the BIGSERIAL on order_status_history so
	# a fresh test run sees ids starting at 1.
	@docker exec booking_db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "TRUNCATE TABLE orders, order_status_history RESTART IDENTITY;"
	@docker exec booking_db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "TRUNCATE events_outbox;"
	@docker exec booking_db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "DELETE FROM events;"
	# Redis: precise DEL of CACHE keys ONLY — never `FLUSHALL`.
	#
	# `FLUSHALL` deletes the Redis Streams (`orders:stream`, `orders:dlq`)
	# AND the consumer group (`orders:group`). Producers and the worker
	# are likely still active during the reset; if FLUSHALL races with an
	# in-flight Lua deduct, the worker self-heals NOGROUP by recreating
	# the group with `XGROUP CREATE ... $` — and `$` SKIPS any messages
	# that already arrived in the stream between FLUSHALL and recreation.
	# Empirically validated 2026-05-03: 411 of 1000 successful Lua
	# deducts had no DB row, no DLQ entry, no log trace — the messages
	# were silently dropped by the self-heal. Full investigation in
	# `docs/architectural_backlog.md` § "Cache-truth architecture".
	#
	# So: target ONLY application cache keys here. Stream / consumer-group
	# / DLQ structures stay intact. Their messages get processed cleanly
	# on the next test run because the worker's Subscribe loop never had
	# a NOGROUP event to recover from.
	@docker exec -e PASS=$(REDIS_PASSWORD) booking_redis sh -c '\
		for pat in "ticket_type_qty:*" "ticket_type_meta:*" "idempotency:*" "saga:reverted:*"; do \
			redis-cli -a "$$PASS" --no-auth-warning --scan --pattern "$$pat" \
				| xargs -r redis-cli -a "$$PASS" --no-auth-warning DEL > /dev/null ; \
		done'
	@echo "DB + Redis cache keys reset (orders:stream / orders:group preserved). Create a fresh event via POST /api/v1/events for the next test run."

help: ## Show help message
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

stop: ## Stop the API server
	@echo "Stopping $(PROJECT_NAME)..."
	@-pkill -f "$(PROJECT_NAME) server" && echo "Stopped" || echo "Not running"

restart: stop run-server ## Restart the API server

# Database Migrations
# MIGRATE_DB_URL is loaded from .env (see .env.example). This is the host-side URL
# (localhost:5433), NOT the in-compose DATABASE_URL which uses `postgres` hostname.
DB_URL=$(MIGRATE_DB_URL)

migrate-up: ## Run all new database migrations
	@test -n "$(DB_URL)" || { echo "MIGRATE_DB_URL is not set. Copy .env.example to .env and fill it in."; exit 1; }
	migrate -path deploy/postgres/migrations -database "$(DB_URL)" up

migrate-down: ## Revert the last database migration
	migrate -path deploy/postgres/migrations -database "$(DB_URL)" down 1

migrate-create: ## Create a new database migration
	@read -p "Enter migration name: " name; \
	migrate create -ext sql -dir deploy/postgres/migrations -seq $$name

migrate-force: ## Force set migration version
	@read -p "Enter version: " version; \
	migrate -path deploy/postgres/migrations -database "$(DB_URL)" force $$version

migrate-status: ## Show the current migration version (N5)
	@test -n "$(DB_URL)" || { echo "MIGRATE_DB_URL is not set. Copy .env.example to .env and fill it in."; exit 1; }
	migrate -path deploy/postgres/migrations -database "$(DB_URL)" version
