# ============================================================
#  Recommendation Service — Makefile
#  Requires: Go 1.26+, Docker Desktop, k6 (for load tests)
#  All targets work in Git Bash / WSL on Windows 11
#
#  ┌─────────────────────────────────────────────────────┐
#  │  ONE-COMMAND STARTUP (recommended)                  │
#  │    make start                                       │
#  │                                                     │
#  │  What it does:                                      │
#  │    1. Build app image                               │
#  │    2. Start app + postgres:15 + redis:7 containers  │
#  │       (all on the same Docker network)              │
#  │    3. App auto-runs migrations on startup           │
#  │    4. App auto-seeds DB if empty                    │
#  │    5. Server ready on http://localhost:8080         │
#  └─────────────────────────────────────────────────────┘
# ============================================================

# -- Default env (for local-only targets) --------------------
DATABASE_URL ?= postgresql://user:password@localhost:5432/recommendations?sslmode=disable
REDIS_URL    ?= redis://localhost:6379
SERVER_PORT  ?= 8080
BINARY       := bin/server

.DEFAULT_GOAL := help

.PHONY: help start stop restart logs \
        build run vet lint test \
        seed seed-docker \
        k6-load k6-batch k6-cache \
        clean

# ── Help ────────────────────────────────────────────────────
help:
	@echo ""
	@echo "  Recommendation Service"
	@echo "  ────────────────────────────────────────────"
	@echo "  Quick start (Docker — ONE command)"
	@echo "    make start          Build & start all services, ready to test"
	@echo "    make stop           Stop & remove containers"
	@echo "    make restart        Rebuild app image and restart (keep DB data)"
	@echo "    make logs           Tail app container logs"
	@echo ""
	@echo "  Local development (requires local Postgres + Redis)"
	@echo "    make build          Compile binary → $(BINARY)"
	@echo "    make run            Build & run server locally"
	@echo "    make vet            go vet"
	@echo "    make lint           go vet + staticcheck (if installed)"
	@echo "    make test           Unit tests with race detector"
	@echo "    make seed           Re-seed local DB (TRUNCATE + insert)"
	@echo "    make seed-docker    Re-seed DB inside the running Docker container"
	@echo ""
	@echo "  Performance tests (k6 required, service must be running)"
	@echo "    make k6-load        Single-user load test  (ramp to 100 VUs, 1 min)"
	@echo "    make k6-batch       Batch endpoint stress test"
	@echo "    make k6-cache       Cache hit-rate effectiveness test"
	@echo ""
	@echo "  Housekeeping"
	@echo "    make clean          Remove build artifacts"
	@echo ""

# ── Docker (main workflow) ───────────────────────────────────

# Start everything with one command.
# Postgres + Redis + App all run on the same Docker bridge network.
# App startup sequence: run migrations → seed if empty → HTTP :8080
start:
	@echo "› Building and starting all services..."
	docker compose up --build -d
	@echo ""
	@echo "  ✓ Services running:"
	@echo "    App      → http://localhost:$(SERVER_PORT)"
	@echo "    Postgres → localhost:5432 (container: postgres)"
	@echo "    Redis    → localhost:6379 (container: redis)"
	@echo ""
	@echo "  Quick test:"
	@echo "    curl http://localhost:$(SERVER_PORT)/health"
	@echo "    curl http://localhost:$(SERVER_PORT)/users/1/recommendations"
	@echo ""

stop:
	@echo "› Stopping services..."
	docker compose down

# Rebuild only the app image, keep DB/Redis data intact.
restart:
	@echo "› Rebuilding app image (DB data preserved)..."
	docker compose up --build -d app

logs:
	docker compose logs -f app

# Re-seed inside the running container (uses the container's DATABASE_URL).
seed-docker:
	@echo "› Re-seeding via Docker container..."
	docker compose exec app sh -c 'DATABASE_URL=$$DATABASE_URL go run /root/scripts/seed.go' 2>/dev/null || \
	docker compose run --rm -e DATABASE_URL=postgresql://user:password@postgres:5432/recommendations?sslmode=disable \
		app go run ./scripts/seed.go

# ── Local development ────────────────────────────────────────
build:
	@echo "› Building binary..."
	@mkdir -p bin
	go build -o $(BINARY) ./cmd/server

run: build
	@echo "› Starting server on :$(SERVER_PORT)..."
	DATABASE_URL=$(DATABASE_URL) REDIS_URL=$(REDIS_URL) SERVER_PORT=$(SERVER_PORT) ./$(BINARY)

vet:
	@echo "› go vet..."
	go vet ./...

lint: vet
	@echo "› staticcheck (skipped if not installed)..."
	@command -v staticcheck >/dev/null 2>&1 \
		&& staticcheck ./... \
		|| echo "  staticcheck not found — install: go install honnef.co/go/tools/cmd/staticcheck@latest"

test:
	@echo "› Running tests with race detector..."
	go test -race -count=1 ./...

seed:
	@echo "› Re-seeding local database..."
	DATABASE_URL=$(DATABASE_URL) go run ./scripts/seed.go

# ── k6 Performance tests ─────────────────────────────────────
k6-load:
	@echo "› Single-user load test (ramp to 100 VUs)..."
	k6 run tests/k6/load_test.js

k6-batch:
	@echo "› Batch endpoint stress test..."
	k6 run tests/k6/batch_test.js

k6-cache:
	@echo "› Cache hit-rate effectiveness test..."
	k6 run tests/k6/cache_test.js

# ── Housekeeping ─────────────────────────────────────────────
clean:
	@echo "› Cleaning..."
	rm -rf bin/
	go clean -cache
