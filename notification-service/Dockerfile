# syntax=docker/dockerfile:1
# ─────────────────────────────────────────────────────────────────────────────
# Build context MUST be the repo root (parent of this dir) because go.mod uses
#   replace github.com/hiabhi-cpu/shared => ../shared
# so the builder needs both ./shared and ./notification-service. Via compose this is handled by
# `context: ..`. Manual build from the repo root:
#   docker build -f notification-service/Dockerfile -t notification-service .
# ─────────────────────────────────────────────────────────────────────────────

FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

# ── Dependency layer ──────────────────────────────────────────────────────────
# Copy only go.mod/go.sum (this module + the replaced shared module) and download
# BEFORE copying source, so editing code does not bust the module-download cache.
COPY shared/go.mod shared/go.sum ./shared/
COPY notification-service/go.mod notification-service/go.sum ./notification-service/
WORKDIR /app/notification-service
RUN go mod download

# ── Source + build ────────────────────────────────────────────────────────────
WORKDIR /app
COPY shared/ ./shared/
COPY notification-service/ ./notification-service/
WORKDIR /app/notification-service
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /out/notification-service ./cmd/main.go

# ── Runner ────────────────────────────────────────────────────────────────────
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/notification-service .
RUN adduser -D appuser
USER appuser
EXPOSE 9004
CMD ["./notification-service"]
