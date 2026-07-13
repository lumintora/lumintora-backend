# ── Build stage ──────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Download deps before copying source so this layer is cached unless
# go.mod / go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -extldflags=-static" \
    -o lumintora-api ./services/monolith/cmd/server

# ── Runtime stage ─────────────────────────────────────────────────
FROM alpine:3.20

# ca-certificates  → outbound HTTPS (CF Worker AI, Wandbox)
# tzdata           → time-zone data for accurate timestamps
RUN apk --no-cache add ca-certificates tzdata && \
    # Non-root user for security
    addgroup -g 1001 lumintora && \
    adduser  -u 1001 -G lumintora -s /bin/sh -D lumintora

WORKDIR /app
COPY --from=builder /app/lumintora-api .
RUN chown lumintora:lumintora /app/lumintora-api

USER lumintora

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=5s --start-period=8s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./lumintora-api"]
