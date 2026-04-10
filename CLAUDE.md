# CLAUDE.md

## Commands

Common commands are listed in the Makefile.

The test-ui (Vite dev server) is at **http://localhost:5173**.
The API is at **http://localhost:8080** (overridable via `HTTP_PORT` in `.env`).

## Architecture

### Request flow

```
HTTP/WS request
  → chi router (internal/app/app.go)
  → handler (internal/handler/game.go or question_list.go)
  → game.Engine (internal/game/engine.go)      ← all runtime game state lives here
  → game.Broadcaster (internal/ws/hub.go)      ← fan-out to WS clients
  → postgres.DB (internal/postgres/)           ← persistence (best-effort, non-fatal)
```

### Catalog vs runtime separation

**Catalog** (persisted in Postgres):
- `question_lists` table — name, visibility, owner
- `question_list_questions` table — ordered questions per list

**Runtime** (in-memory only):
- `game.Engine` — holds a copy of the questions loaded from the list at game creation, plus player state and answers
- Questions are copied from the catalog once when `POST /games` is called; the engine never reads Postgres again

This means game state survives temporary DB outages, but does not survive a server restart.

### Game state model

Game state is **in-memory only** (`game.Engine`, one per game). The engine holds a `sync.RWMutex`. The critical rule: **events are broadcast after releasing the lock** to avoid contention with the hub. Any method that mutates state follows the pattern: lock → mutate → collect event data → unlock → broadcast.

`game.Manager` is the in-memory registry of all active engines.

### Redis pub/sub broadcaster

`redis.PubSubBroadcaster` implements `game.Broadcaster` via Redis pub/sub:
- `Broadcast` / `BroadcastTo` → publish to channel `game:<id>:events`
- A subscriber goroutine reads from that channel → forwards to the local `ws.Hub` → WS clients

This makes the event path horizontally scalable: multiple API instances subscribe to the same channel and each forwards to their own WS connections. The broadcaster is created per-game in `app.gameSessionStore.GetOrCreate`. All broadcasters are stopped (`Stop()`) during graceful shutdown.

**Note:** `game_joined` is sent directly via `hub.BroadcastTo` (bypassing Redis) because it is a connection handshake that must arrive immediately and synchronously before the read/write pumps start.

### Postgres persistence

Writes are best-effort (non-fatal — logged but don't abort requests):

| Operation               | Persisted fields                          |
|-------------------------|-------------------------------------------|
| `POST /games`           | game row + owner player row               |
| `POST /games/{id}/join` | player row                                |
| `POST /games/{id}/start`| `games.status = 'running'`                |
| `POST /games/{id}/close`| `players.lives` + `players.active` for every player who lost a life; `games.status = 'finished'` if game over |

### Question lists and access rules

`QuestionListStore` (implemented by `postgres.DB`) manages the catalog:
- Public lists: created by `admin` actors, readable by all
- Private lists: created by `user` actors, visible only to the owning actor

Auth is **not implemented**. Identity is simulated via `X-Debug-Actor-Type` / `X-Debug-Actor-Id` headers — see `internal/handler/actor.go`. Replace this with real middleware before production.

### WebSocket lifecycle

Each game has one `ws.Hub` (registered in `app.hubStore`). When a player connects to `/ws?gameId=&playerId=`:
1. The handler looks up the engine and verifies the player exists.
2. A `ws.Client` is created (buffered send channel, 256 msgs).
3. `WritePump` and `ReadPump` run in separate goroutines.
4. `game_joined` is sent to the player immediately.
5. Incoming client messages (`submit_answer`) are routed back to the engine.

### Migrations

SQL is embedded in the binary via `//go:embed` in `migrations/migrations.go`. Two files are concatenated: `001_initial.sql` (base tables) and `002_question_lists.sql` (catalog tables + `question_list_id` column on `games`). `app.New()` calls `pg.RunMigrations()` on every startup — safe because all statements use `IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`.

To add a migration: create `00N_name.sql`, embed it in `migrations.go`, and append it to `SQL`.

### Test-UI (tools/test-ui)

Vite + Vue 3 + TypeScript dev tool, not production code. All HTTP calls go through Vite's proxy (`/api/*` → backend, `/ws` → backend WS) so there are no CORS issues. `VITE_API_TARGET` controls the proxy target.

The `actor` reactive state (`src/actor.ts`) holds the simulated identity and is injected as headers in every `api.ts` call.

## Key design constraints

- `game.Engine` has no knowledge of transport (HTTP/WS) or storage — it takes a `Broadcaster` interface.
- `ws.Hub` implements `game.Broadcaster` — the only coupling point between game logic and WebSocket.
- `store.GameStore` / `PlayerStore` / `QuestionListStore` are interfaces; `postgres.DB` implements all three. Passing `nil` for the stores in tests is valid.
- `game.Engine.AddQuestion` is kept for test convenience only — HTTP no longer exposes it. Production games get their questions from the list at creation time.
- The `go build` in `.air.toml` uses `-buildvcs=false` — required because the container can't access git metadata.
