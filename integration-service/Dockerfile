# syntax=docker/dockerfile:1
# Build context MUST be the repo root (go.mod uses replace ../shared).
#   docker build -f integration-service/Dockerfile -t integration-service .
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY shared/go.mod shared/go.sum ./shared/
COPY integration-service/go.mod integration-service/go.sum ./integration-service/
WORKDIR /app/integration-service
RUN go mod download
WORKDIR /app
COPY shared/ ./shared/
COPY integration-service/ ./integration-service/
WORKDIR /app/integration-service
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /out/integration-service ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/integration-service .
RUN adduser -D appuser
USER appuser
EXPOSE 9009 9443
CMD ["./integration-service"]
