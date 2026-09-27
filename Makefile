.PHONY: up up-multi down logs ui-logs test fmt lint migrate-up build load-test load-test-game load-test-room load-test-multi shell

# ── Docker Compose ──────────────────────────────────────────────────────────

up:
	docker compose up --build -d

# Same stack plus a second API instance (api2, port HTTP_PORT_2, default 8081).
up-multi:
	docker compose --profile multi up --build -d

down:
	docker compose --profile multi down

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

# ── Load tests (requires k6) ────────────────────────────────────────────────
# HTTP_PORT is read from .env; falls back to 8080 if .env is absent.
# Override BASE_URL / WS_URL to target a non-local environment.
# Example: make load-test-game BASE_URL=http://api:8080 WS_URL=ws://api:8080

-include .env
HTTP_PORT ?= 8080
BASE_URL  ?= http://localhost:$(HTTP_PORT)
WS_URL    ?= ws://localhost:$(HTTP_PORT)

load-test-game:
	k6 run k6/game_flow.js -e BASE_URL=$(BASE_URL) -e WS_URL=$(WS_URL)

NUM_PLAYERS    ?= 50
ANSWER_MIN_MS  ?= 200
ANSWER_MAX_MS  ?= 2000
CLOSE_DELAY_MS ?= 500

load-test-room:
	k6 run k6/room_load.js \
		-e BASE_URL=$(BASE_URL) \
		-e WS_URL=$(WS_URL) \
		-e NUM_PLAYERS=$(NUM_PLAYERS) \
		-e ANSWER_MIN_MS=$(ANSWER_MIN_MS) \
		-e ANSWER_MAX_MS=$(ANSWER_MAX_MS) \
		-e CLOSE_DELAY_MS=$(CLOSE_DELAY_MS)

# One game spread over api and api2 (start them with `make up-multi`).
HTTP_PORT_2 ?= 8081
load-test-multi:
	k6 run k6/multi_instance.js 		-e BASE_URLS=http://localhost:$(HTTP_PORT),http://localhost:$(HTTP_PORT_2) 		-e WS_URLS=ws://localhost:$(HTTP_PORT),ws://localhost:$(HTTP_PORT_2)

load-test: load-test-game load-test-room

# ── Shell into the running API container ────────────────────────────────────

shell:
	docker compose exec api sh
