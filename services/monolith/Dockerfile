# Build from the repo root: docker build -f services/monolith/Dockerfile .
FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -extldflags=-static" \
    -o lumintora-api ./services/monolith/cmd/server

FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -g 1001 lumintora && \
    adduser  -u 1001 -G lumintora -s /bin/sh -D lumintora

WORKDIR /app
COPY --from=builder /app/lumintora-api .
RUN chown lumintora:lumintora /app/lumintora-api

USER lumintora

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["./lumintora-api"]
