# ── DPDP auth-service ──────────────────────────────────────────────────────
# Repo: github.com/hiabhi-cpu/auth-service
# Port: 9006

.PHONY: run build test tidy migrate-up migrate-down migrate-status gen-keys

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

## build: compile binary to bin/auth-service
build:
	mkdir -p bin
	go build -o bin/auth-service ./cmd/...
	@echo "✅ bin/auth-service"

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

## gen-keys: generate RSA 2048 key pair for JWT signing (run once)
gen-keys:
	@mkdir -p keys
	@openssl genrsa -out keys/auth_private.pem 2048
	@openssl rsa -in keys/auth_private.pem -pubout -out keys/auth_public.pem
	@echo "✅ keys/auth_private.pem (KEEP SECRET — never commit)"
	@echo "✅ keys/auth_public.pem  (share with all services)"
