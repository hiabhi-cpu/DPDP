# ── DPDP audit-service ─────────────────────────────────────────────────────
# Repo: github.com/hiabhi-cpu/audit-service
# Port: 9001

.PHONY: run build test tidy migrate-up migrate-down migrate-status

# Load .env if it exists
ifneq (,$(wildcard ./.env))
  include .env
  export
endif

GOOSE_DSN := postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@$(POSTGRES_HOST):$(POSTGRES_PORT)/$(POSTGRES_DB)?sslmode=disable
export PATH := $(PATH):$(shell go env GOPATH)/bin

## run: run the service in dev mode (no binary saved)
run:
	go run ./cmd/...

## build: compile binary to bin/audit-service
build:
	mkdir -p bin
	go build -o bin/audit-service ./cmd/...
	@echo "✅ bin/audit-service"

## test: run all tests with race detector
test:
	go test ./... -v -race

## tidy: tidy go modules
tidy:
	go mod tidy

## migrate-up: apply pending migrations
migrate-up:
	@goose -dir db/migrations postgres "$(GOOSE_DSN)" up

## migrate-down: rollback last migration
migrate-down:
	@goose -dir db/migrations postgres "$(GOOSE_DSN)" down

## migrate-status: show migration status
migrate-status:
	@goose -dir db/migrations postgres "$(GOOSE_DSN)" status
