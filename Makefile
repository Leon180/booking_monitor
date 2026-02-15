PROJECT_NAME := booking-cli
BUILD_DIR := bin
CMD_PATH := ./cmd/booking-cli

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
	go test -race -v ./internal/... ./pkg/...

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

mocks: ## Generate mocks
	@go generate ./...

reset-db: ## Reset database (clear orders, reset event inventory to 100)
	@echo "Resetting database..."
	@docker exec booking_db psql -U user -d booking -c "TRUNCATE orders;"
	@docker exec booking_db psql -U user -d booking -c "DELETE FROM events WHERE id != 1;"
	@docker exec booking_db psql -U user -d booking -c "UPDATE events SET total_tickets = 100, available_tickets = 100, name = 'Jay Chou Concert', version = 0 WHERE id = 1;"
	@docker exec booking_db psql -U user -d booking -c "INSERT INTO events (id, name, total_tickets, available_tickets, version) VALUES (1, 'Jay Chou Concert', 100, 100, 0) ON CONFLICT (id) DO NOTHING;"
	@echo "Database reset complete."

help: ## Show help message
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

stop: ## Stop the API server
	@echo "Stopping $(PROJECT_NAME)..."
	@-pkill -f "$(PROJECT_NAME) server" && echo "Stopped" || echo "Not running"

restart: stop run-server ## Restart the API server

# Database Migrations
DB_URL=postgres://user:password@localhost:5433/booking?sslmode=disable

migrate-up: ## Run all new database migrations
	migrate -path deploy/postgres/migrations -database "$(DB_URL)" up

migrate-down: ## Revert the last database migration
	migrate -path deploy/postgres/migrations -database "$(DB_URL)" down 1

migrate-create: ## Create a new database migration
	@read -p "Enter migration name: " name; \
	migrate create -ext sql -dir deploy/postgres/migrations -seq $$name

migrate-force: ## Force set migration version
	@read -p "Enter version: " version; \
	migrate -path deploy/postgres/migrations -database "$(DB_URL)" force $$version

