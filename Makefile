.PHONY: up down logs ui-logs test fmt lint migrate-up migrate-down build

# ── Docker Compose ──────────────────────────────────────────────────────────

up:
	docker compose up --build -d

down:
	docker compose down

logs:
	docker compose logs -f api

ui-logs:
	docker compose logs -f ui

# ── Build ───────────────────────────────────────────────────────────────────

build:
	go build -o bin/api ./cmd/api

# ── Tests ───────────────────────────────────────────────────────────────────

test:
	go test ./... -race -count=1

# ── Code quality ────────────────────────────────────────────────────────────

fmt:
	gofmt -w .
	goimports -w . 2>/dev/null || true

lint:
	golangci-lint run ./... 2>/dev/null || go vet ./...

# ── Migrations ──────────────────────────────────────────────────────────────
# Migrations run automatically at startup (embedded SQL, applied via pgx).
# Restarting the api container is enough to apply any new migration.

migrate-up:
	docker compose restart api

# ── Shell into the running API container ────────────────────────────────────

shell:
	docker compose exec api sh
