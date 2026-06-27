.PHONY: build build-auth build-core build-ai build-monolith run-auth run-core run-ai run test tidy

build: build-auth build-core build-ai build-monolith

build-monolith:
	go build -o bin/lumintora-api ./services/monolith/cmd/server

run:
	PORT=8080 go run ./services/monolith/cmd/server

build-auth:
	go build -o bin/auth-service ./services/auth-service/cmd/server

build-core:
	go build -o bin/core-api ./services/core-api/cmd/server

build-ai:
	go build -o bin/ai-service ./services/ai-service/cmd/server

run-auth:
	PORT=8081 go run ./services/auth-service/cmd/server

run-core:
	PORT=8082 go run ./services/core-api/cmd/server

run-ai:
	PORT=8083 go run ./services/ai-service/cmd/server

test:
	go test ./...

tidy:
	go mod tidy
