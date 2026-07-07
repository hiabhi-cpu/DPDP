# syntax=docker/dockerfile:1
# ─────────────────────────────────────────────────────────────────────────────
# Build context MUST be the repo root (parent of this dir) because go.mod uses
#   replace github.com/hiabhi-cpu/shared => ../shared
# so the builder needs both ./shared and ./audit-service. Via compose this is handled by
# `context: ..`. Manual build from the repo root:
#   docker build -f audit-service/Dockerfile -t audit-service .
# ─────────────────────────────────────────────────────────────────────────────

FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

# ── Dependency layer ──────────────────────────────────────────────────────────
# Copy only go.mod/go.sum (this module + the replaced shared module) and download
# BEFORE copying source, so editing code does not bust the module-download cache.
COPY shared/go.mod shared/go.sum ./shared/
COPY audit-service/go.mod audit-service/go.sum ./audit-service/
WORKDIR /app/audit-service
RUN go mod download

# ── Source + build ────────────────────────────────────────────────────────────
WORKDIR /app
COPY shared/ ./shared/
COPY audit-service/ ./audit-service/
WORKDIR /app/audit-service
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /out/audit-service ./cmd/main.go

# ── Runner ────────────────────────────────────────────────────────────────────
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/audit-service .
RUN adduser -D appuser
USER appuser
EXPOSE 9001
CMD ["./audit-service"]
