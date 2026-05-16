# =============================================================================
# QUICK REFERENCE — copy-paste these commands in your terminal
# =============================================================================
#
#   make build                          → build all 8 services into bin/
#   make build-one SVC=consent-service  → build one specific service
#
#   make run SVC=consent-service        → run one service (dev, no binary saved)
#   make run-all                        → run all 8 services in background
#   make stop-all                       → kill all background services
#
#   make tidy                           → go mod tidy in all services
#   make help                           → list all targets
#
# =============================================================================
# =============================================================================
# VARIABLES
# := means "assign this value now" (eagerly evaluated)
# Use variables later with $(VARIABLE_NAME)
# Think of it like: const SERVICES = "..." in Go
# =============================================================================

# List of all microservices — used in loops below
# The \ at end of line means "continue on next line" (line continuation)
SERVICES := consent-service audit-service withdrawal-service emergency-service \
            notification-service report-service auth-service integration-service

# Output directory where compiled binaries will be placed
BIN_DIR := bin

# File where background service PIDs are saved by run-all
PID_FILE := .service-pids

# Ports each service listens on (consent=9000, audit=9001, ... integration=9007)
# Used by stop-all to kill whatever process is actually bound to each port
PORTS := 9000 9001 9002 9003 9004 9005 9006 9007


# =============================================================================
# TARGETS
# Structure of every target:
#
#   target-name: [optional dependencies]
#   <TAB> shell command      ← MUST be a TAB, not spaces
#   <TAB> shell command
#
# Run with: make <target-name>
# =============================================================================


# .PHONY tells make "this is NOT a file, always run this target"
# Without it, if a file named 'build' exists on disk, make would skip the target
## build: compile all services into bin/
.PHONY: build
build:
	# mkdir -p creates the bin/ folder (and parent dirs) if it doesn't exist
	# -p means: don't error if folder already exists
	mkdir -p $(BIN_DIR)

	# WHY A LOOP: ./services/... doesn't work across multiple go.mod files.
	# Each service is its own Go module, so we must build them one by one.
	# We cd into each service dir so go build finds the right go.mod.
	@for svc in $(SERVICES); do \
		echo "🔨 Building $$svc..."; \
		(cd services/$$svc && go build -o ../../$(BIN_DIR)/$$svc .); \
	done

	# @ prefix means: run this command silently (don't print the command itself, only its output)
	@echo "✅ All services built in ./bin/"


## build-one: compile a single service  →  make build-one SVC=consent-service
.PHONY: build-one
build-one:
	mkdir -p $(BIN_DIR)

	# $(SVC) is a variable passed at runtime: make build-one SVC=consent-service
	# expands to: go build -o bin/consent-service ./services/consent-service
	go build -o $(BIN_DIR)/$(SVC) ./services/$(SVC)

	@echo "✅ Built ./bin/$(SVC)"


## run: run a single service without compiling a binary  →  make run SVC=consent-service
.PHONY: run
run:
	# go run compiles and runs in one step — no binary is saved to disk
	# Good for development: fast, no cleanup needed
	# $(SVC) is passed at runtime: make run SVC=auth-service
	go run ./services/$(SVC)


## run-all: run all 8 services in the background (dev only)
.PHONY: run-all
run-all:
	# Clear any old PID file before starting fresh
	@rm -f $(PID_FILE)
	# @for loops over every service name in $(SERVICES)
	# $$ is how you write a shell variable inside a Makefile (single $ is reserved for make)
	# & at the end of a command means "run in background" (don't wait for it to finish)
	# $$! captures the PID of the last background process and saves it to PID_FILE
	@for svc in $(SERVICES); do \
		echo "🚀 Starting $$svc..."; \
		go run ./services/$$svc & \
		echo $$! >> $(PID_FILE); \
	done
	@echo "All services started. PIDs saved to $(PID_FILE). Use 'make stop-all' to stop."


## stop-all: kill whatever process is listening on each service port (9000-9007)
.PHONY: stop-all
stop-all:
	# WHY PORT-BASED: 'go run' spawns a child process (the actual compiled binary).
	# Killing the 'go run' PID leaves the real server still running on the port.
	# fuser -k <port>/tcp kills whatever process is actually bound to that port.
	# || true prevents make from failing if a port has no process on it.
	@echo "🛑 Killing processes on ports 9000–9007..."
	@for port in $(PORTS); do \
		fuser -k $$port/tcp 2>/dev/null || true; \
	done
	@rm -f $(PID_FILE)
	@echo "🛑 All services stopped."


## tidy: run 'go mod tidy' in every service AND the shared module
.PHONY: tidy
tidy:
	# Tidy the shared module first (services may depend on it)
	@echo "🔧 Tidying shared..."
	@(cd shared && go mod tidy)
	# cd into each service dir and run go mod tidy
	# go mod tidy: adds missing deps and removes unused ones from go.mod + go.sum
	# Wrapping in () means the cd only affects that subshell, not the current shell
	@for svc in $(SERVICES); do \
		echo "🔧 Tidying $$svc..."; \
		(cd services/$$svc && go mod tidy); \
	done
	@echo "✅ All modules tidied."

## gen-keys: generate RSA key pair for auth-service JWT signing (run once for local dev)
.PHONY: gen-keys
gen-keys:
	@echo "🔑 Generating RSA 2048 key pair for auth-service..."
	@mkdir -p services/auth-service/keys
	@openssl genrsa -out services/auth-service/keys/auth_private.pem 2048
	@openssl rsa -in services/auth-service/keys/auth_private.pem -pubout \
		-out services/auth-service/keys/auth_public.pem
	@echo "✅ Keys written to services/auth-service/keys/"
	@echo "   🔒 auth_private.pem — sign tokens (KEEP SECRET, never commit)"
	@echo "   🔓 auth_public.pem  — verify tokens (safe to commit, share with services)"

## help: list all available make targets with descriptions
.PHONY: help
help:
	# grep finds all lines starting with ## in this Makefile
	# sed strips the ## prefix so only the description is shown
	@grep -E '^## ' Makefile | sed 's/## //'


# =============================================================================
# DATABASE — Docker Compose + PostgreSQL
# Requires: docker, docker compose
# Load .env automatically so POSTGRES_* vars are available
# =============================================================================

# Load .env file if it exists (exposes vars to make targets)
ifneq (,$(wildcard ./.env))
  include .env
  export
endif

## db-up: start PostgreSQL in Docker (background)
.PHONY: db-up
db-up:
	@echo "🐘 Starting PostgreSQL..."
	@docker compose up -d postgres
	@echo "⏳ Waiting for postgres to be healthy..."
	@until docker compose exec postgres pg_isready -U $(POSTGRES_USER) -d $(POSTGRES_DB) > /dev/null 2>&1; do \
		sleep 1; \
	done
	@echo "✅ PostgreSQL is ready at localhost:$(POSTGRES_PORT)"


## db-down: stop PostgreSQL container (data is preserved in volume)
.PHONY: db-down
db-down:
	@echo "🛑 Stopping PostgreSQL..."
	@docker compose down
	@echo "✅ PostgreSQL stopped."


## db-reset: stop and WIPE all data (drops the volume) — use with caution!
.PHONY: db-reset
db-reset:
	@echo "⚠️  WARNING: This will DELETE all data. Press Ctrl+C to cancel..."
	@sleep 3
	@docker compose down -v
	@echo "🗑️  Data wiped. Run 'make db-up' to start fresh."


## db-logs: follow PostgreSQL container logs
.PHONY: db-logs
db-logs:
	@docker compose logs -f postgres


## db-shell: open a psql shell inside the postgres container
.PHONY: db-shell
db-shell:
	@docker compose exec postgres psql -U $(POSTGRES_USER) -d $(POSTGRES_DB)


# =============================================================================
# GOOSE MIGRATIONS
# Requires: goose → go install github.com/pressly/goose/v3/cmd/goose@latest
# SVC variable selects which service's migrations to run (default: auth-service)
# =============================================================================

# Default service for migration commands
SVC ?= auth-service

# Add GOPATH/bin to PATH so goose and sqlc (installed via `go install`) are found
export PATH := $(PATH):$(shell go env GOPATH)/bin

GOOSE_DSN := postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@$(POSTGRES_HOST):$(POSTGRES_PORT)/$(POSTGRES_DB)?sslmode=disable
MIGRATIONS_DIR := services/$(SVC)/db/migrations

## migrate-up: apply all pending migrations  →  make migrate-up SVC=auth-service
.PHONY: migrate-up
migrate-up:
	@echo "⬆️  Running migrations for $(SVC)..."
	@goose -dir $(MIGRATIONS_DIR) postgres "$(GOOSE_DSN)" up
	@echo "✅ Migrations applied."


## migrate-down: rollback the last migration  →  make migrate-down SVC=auth-service
.PHONY: migrate-down
migrate-down:
	@echo "⬇️  Rolling back last migration for $(SVC)..."
	@goose -dir $(MIGRATIONS_DIR) postgres "$(GOOSE_DSN)" down
	@echo "✅ Rolled back."


## migrate-status: show migration status  →  make migrate-status SVC=auth-service
.PHONY: migrate-status
migrate-status:
	@goose -dir $(MIGRATIONS_DIR) postgres "$(GOOSE_DSN)" status


## migrate-reset: rollback ALL migrations (wipes schema tables, keeps DB)
.PHONY: migrate-reset
migrate-reset:
	@echo "⚠️  Resetting all migrations for $(SVC)..."
	@goose -dir $(MIGRATIONS_DIR) postgres "$(GOOSE_DSN)" reset


# =============================================================================
# SQLC — Generate type-safe Go code from SQL queries
# Requires: sqlc → go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
# =============================================================================

## sqlc: generate Go code from SQL queries  →  make sqlc SVC=auth-service
.PHONY: sqlc
sqlc:
	@echo "⚙️  Generating SQLC code for $(SVC)..."
	@(cd services/$(SVC) && sqlc generate)
	@echo "✅ SQLC code generated in services/$(SVC)/db/sqlc/"


## sqlc-verify: check sqlc config without generating (CI-safe)
.PHONY: sqlc-verify
sqlc-verify:
	@echo "🔍 Verifying SQLC config for $(SVC)..."
	@(cd services/$(SVC) && sqlc vet)


# =============================================================================
# SETUP — Install required CLI tools (one-time)
# =============================================================================

## tools: install goose and sqlc CLI tools
.PHONY: tools
tools:
	@echo "📦 Installing goose..."
	@go install github.com/pressly/goose/v3/cmd/goose@latest
	@echo "📦 Installing sqlc..."
	@go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	@echo "✅ Tools installed. Make sure $$(go env GOPATH)/bin is in your PATH."
