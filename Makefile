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

benchmark: ## Run full benchmark with recording (usage: make benchmark VUS=1000 DURATION=60s)
	@chmod +x scripts/benchmark_k6.sh
	@./scripts/benchmark_k6.sh $(VUS) $(DURATION)

benchmark-compare: ## Run two-run comparison benchmark (usage: make benchmark-compare VUS=500 DURATION=60s)
	@chmod +x scripts/benchmark_compare.sh
	@./scripts/benchmark_compare.sh $(VUS) $(DURATION)

docker-restart: ## Restart the API server in Docker (Rebuild and Up)
	@echo "Restarting booking_app container..."
	@docker-compose stop app
	@docker-compose build app
	@docker-compose up -d app
	@echo "App restarted in Docker."

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

reset-db: ## Reset database — truncate orders/events/outbox + flush Redis. Tests create their own events via POST /api/v1/events
	@echo "Resetting database..."
	# Post-PR-34 events.id is UUID, so the previous "seed event id=1"
	# pattern is gone (and was already obsolete: stress test requires
	# --event-id <uuid>, k6 scripts call POST /api/v1/events in their
	# setup()). Truncate everything; each test run creates a fresh
	# event via the API. Redis is flushed wholesale because the
	# event:{uuid}:qty keys are scoped per event and a stale UUID
	# from a previous run carries no value.
	@docker exec booking_db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "TRUNCATE orders;"
	@docker exec booking_db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "TRUNCATE events_outbox;"
	@docker exec booking_db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "DELETE FROM events;"
	@docker exec booking_redis redis-cli FLUSHALL
	@echo "Database and Redis reset complete. Create a fresh event via POST /api/v1/events for the next test run."

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

