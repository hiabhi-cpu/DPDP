# syntax=docker/dockerfile:1
# Build context MUST be the repo root (parent of this dir) because go.mod uses
#   replace github.com/hiabhi-cpu/shared => ../shared
# Via compose this is handled by `context: ..`. Manual build from the repo root:
#   docker build -f kiosk-bff/Dockerfile -t kiosk-bff .

FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY shared/go.mod shared/go.sum ./shared/
COPY kiosk-bff/go.mod kiosk-bff/go.sum ./kiosk-bff/
WORKDIR /app/kiosk-bff
RUN go mod download
WORKDIR /app
COPY shared/ ./shared/
COPY kiosk-bff/ ./kiosk-bff/
WORKDIR /app/kiosk-bff
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /out/kiosk-bff ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/kiosk-bff .
RUN adduser -D appuser
USER appuser
EXPOSE 9008
CMD ["./kiosk-bff"]
