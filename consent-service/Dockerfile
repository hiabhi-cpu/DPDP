# syntax=docker/dockerfile:1
# ─────────────────────────────────────────────────────────────────────────────
# Build context MUST be the repo root (parent of this dir) because go.mod uses
#   replace github.com/hiabhi-cpu/shared => ../shared
# so the builder needs both ./shared and ./consent-service. Via compose this is handled by
# `context: ..`. Manual build from the repo root:
#   docker build -f consent-service/Dockerfile -t consent-service .
# ─────────────────────────────────────────────────────────────────────────────

FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

# ── Dependency layer ──────────────────────────────────────────────────────────
# Copy only go.mod/go.sum (this module + the replaced shared module) and download
# BEFORE copying source, so editing code does not bust the module-download cache.
COPY shared/go.mod shared/go.sum ./shared/
COPY consent-service/go.mod consent-service/go.sum ./consent-service/
WORKDIR /app/consent-service
RUN go mod download

# ── Source + build ────────────────────────────────────────────────────────────
WORKDIR /app
COPY shared/ ./shared/
COPY consent-service/ ./consent-service/
WORKDIR /app/consent-service
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /out/consent-service ./cmd/main.go

# ── Runner ────────────────────────────────────────────────────────────────────
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/consent-service .
RUN adduser -D appuser
USER appuser
EXPOSE 9000
CMD ["./consent-service"]
