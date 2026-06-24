.PHONY: build test vet lint run up down docker tidy

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# Run locally against a Redis on localhost:6379.
run:
	REDIS_ADDR=localhost:6379 KUDZU_WRITE_TOKENS=local-dev-token go run ./cmd/kudzu

# Bring the local Kudzu + Redis stack up / down.
up:
	docker compose up --build

down:
	docker compose down -v

docker:
	docker build -f deploy/Dockerfile -t kudzu:dev .
