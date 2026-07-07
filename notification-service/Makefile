# ── DPDP notification-service ────────────────────────────────────────────────
# Repo: github.com/hiabhi-cpu/notification-service
# Port: 9004

.PHONY: run build test tidy

# Load .env if it exists
ifneq (,$(wildcard ./.env))
  include .env
  export
endif

export PATH := $(PATH):$(shell go env GOPATH)/bin

## run: run the service in dev mode (no binary saved)
run:
	go run ./cmd/...

## build: compile binary to bin/notification-service
build:
	mkdir -p bin
	go build -o bin/notification-service ./cmd/...
	@echo "✅ bin/notification-service"

## test: run all tests with race detector
test:
	go test ./... -v -race

## tidy: tidy go modules
tidy:
	go mod tidy
