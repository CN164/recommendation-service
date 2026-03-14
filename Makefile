.PHONY: help build run test seed migrate-up migrate-down clean docker-build docker-up docker-down

help:
	@echo "Recommendation Service v2 - Makefile"
	@echo "===================================="
	@echo ""
	@echo "Usage:"
	@echo "  make build          - Build the application"
	@echo "  make run            - Run the application locally"
	@echo "  make test           - Run tests"
	@echo "  make seed           - Seed the database"
	@echo "  make migrate-up     - Run migrations up"
	@echo "  make migrate-down   - Run migrations down"
	@echo "  make clean          - Clean build artifacts"
	@echo "  make docker-build   - Build Docker image"
	@echo "  make docker-up      - Start Docker services"
	@echo "  make docker-down    - Stop Docker services"
	@echo ""

build:
	@echo "Building application..."
	go build -o bin/server ./cmd/server

run: build
	@echo "Running application..."
	./bin/server

test:
	@echo "Running tests..."
	go test -v ./...

seed:
	@echo "Seeding database..."
	go run ./scripts/seed.go

migrate-up:
	@echo "Running migrations up..."
	psql -U user -d recommendations -f migrations/001_init.up.sql

migrate-down:
	@echo "Running migrations down..."
	psql -U user -d recommendations -f migrations/001_init.down.sql

clean:
	@echo "Cleaning build artifacts..."
	rm -rf bin/
	go clean

docker-build:
	@echo "Building Docker image..."
	docker compose build

docker-up:
	@echo "Starting Docker services..."
	docker compose up -d
	@echo ""
	@echo "Services started:"
	@echo "  - App:      http://localhost:8080"
	@echo "  - Postgres: localhost:5432"
	@echo "  - Redis:    localhost:6379"
	@echo ""

docker-down:
	@echo "Stopping Docker services..."
	docker compose down

docker-logs:
	docker compose logs -f app

install-deps:
	@echo "Installing Go dependencies..."
	go mod download
	go mod tidy

.DEFAULT_GOAL := help
