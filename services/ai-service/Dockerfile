# Build from the repo root: docker build -f services/ai-service/Dockerfile .
FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -extldflags=-static" \
    -o ai-service ./services/ai-service/cmd/server

FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -g 1001 lumintora && \
    adduser  -u 1001 -G lumintora -s /bin/sh -D lumintora

WORKDIR /app
COPY --from=builder /app/ai-service .
RUN chown lumintora:lumintora /app/ai-service

USER lumintora

EXPOSE 8083

HEALTHCHECK --interval=10s --timeout=5s --start-period=8s --retries=3 \
  CMD wget -qO- http://localhost:8083/health || exit 1

CMD ["./ai-service"]
