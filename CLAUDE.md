# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Start all services (api + postgres + redis + test-ui)
make up

# Run tests (race detector enabled)
make test

# Run a single test package
go test ./internal/game/... -race -count=1

# Run a single test
go test ./internal/game/... -run TestPlayerEliminated -race -v

# Build locally
make build          # outputs to bin/api

# Format
make fmt            # gofmt + goimports

# Lint
make lint           # golangci-lint, falls back to go vet

# Tail logs
make logs           # api container
make ui-logs        # test-ui container

# Reapply migrations (auto-runs on startup; this just restarts the container)
make migrate-up
```

The test-ui (Vite dev server) is at **http://localhost:5173**.
The API is at **http://localhost:8080** (overridable via `HTTP_PORT` in `.env`).

## Architecture

### Request flow

```
HTTP/WS request
  → chi router (internal/app/app.go)
  → handler (internal/handler/game.go)
  → game.Engine (internal/game/engine.go)   ← all game state lives here
  → game.Broadcaster (internal/ws/hub.go)   ← fan-out to WS clients
  → postgres.DB (internal/postgres/)        ← persistence (best-effort, non-fatal)
```

### Game state model

Game state is **in-memory only** (`game.Engine`, one per game). Postgres and Redis are wired but the engine does not depend on them — the handler calls the store after mutating the engine, and failures are logged but don't abort the request.

The engine holds a `sync.RWMutex`. The critical rule: **events are broadcast after releasing the lock** to avoid contention with the hub. Any method that mutates state follows the pattern: lock → mutate → collect event data → unlock → broadcast.

### WebSocket lifecycle

Each game has one `ws.Hub` (registered in `app.hubStore`). When a player connects to `/ws?gameId=&playerId=`:
1. The handler looks up the engine and verifies the player exists.
2. A `ws.Client` is created (buffered send channel, 256 msgs).
3. `WritePump` and `ReadPump` run in separate goroutines.
4. `game_joined` is sent to the player immediately.
5. Incoming client messages (`submit_answer`) are routed back to the engine.

### Migrations

SQL is embedded in the binary via `//go:embed` in `migrations/migrations.go`. `app.New()` calls `pg.RunMigrations()` on every startup — safe because all statements use `IF NOT EXISTS`. To add a migration: edit `migrations/001_initial.sql` (or add a new file and update the embed + `RunMigrations`).

### Test-UI (tools/test-ui)

Vite + Vue 3 + TypeScript dev tool, not production code. All HTTP calls go through Vite's proxy (`/api/*` → backend, `/ws` → backend WS) so there are no CORS issues. `VITE_API_TARGET` controls the proxy target — set to `http://api:8080` in Docker, `http://localhost:8080` locally.

## Key design constraints

- `game.Engine` has no knowledge of transport (HTTP/WS) or storage — it takes a `Broadcaster` interface.
- `ws.Hub` implements `game.Broadcaster` — the only coupling point between game logic and WebSocket.
- `store.GameStore` / `PlayerStore` / `QuestionStore` are interfaces; `postgres.DB` implements all three. Passing `nil` for the stores in tests is valid.
- The `go build` in `.air.toml` uses `-buildvcs=false` — required because the container can't access git metadata.
