# CLAUDE.md

## Commands

Common commands are listed in the Makefile (`make test` runs `go test ./... -race -count=1`).

The test-ui (Vite dev server) is at **http://localhost:5173**.
The API is at **http://localhost:8080** (overridable via `HTTP_PORT` in `.env`).

## Architecture

### Request flow

```
HTTP/WS request
  → chi router + auth.Middleware (internal/app/app.go, internal/auth/)
  → handler (internal/handler/game.go or question_list.go)
  → game.Engine (internal/game/engine.go)            ← all runtime game state lives here
  → redis.PubSubBroadcaster (internal/redis/)        ← publishes events to game:<id>:events
  → ws.Hub (internal/ws/hub.go)                      ← fan-out to WS clients
  → postgres.DB (internal/postgres/)                 ← persistence (best-effort, non-fatal)
```

### Catalog vs runtime separation

**Catalog** (persisted in Postgres):
- `question_lists` table: name, visibility, owner
- `question_list_questions` table: ordered questions per list, optional `theme_id`
- `themes` table: `question_list_id IS NULL` is a global theme, otherwise a custom theme of that list (exposed as `scope`: `global` / `list`)

**Runtime** (in-memory only):
- `game.Engine`: holds a copy of the questions loaded from the list at game creation, plus player state and answers
- Questions are copied from the catalog once when `POST /games` is called; the engine never reads Postgres again

This means game state survives temporary DB outages, but does not survive a server restart.

### Game state model

Game state is **in-memory only** (`game.Engine`, one per game). The engine holds a `sync.RWMutex`. The critical rule: **events are broadcast after releasing the lock** to avoid contention with the hub. Any method that mutates state follows the pattern: lock → mutate → collect event data → unlock → broadcast.

`Engine.Snapshot()` returns a deep copy (players, questions, answers) so callers can read it after the lock is released. Never hand out pointers into `e.game`.

`game.Manager` is the in-memory registry of all active engines.

Game rules: a wrong or missing answer costs one life, 0 lives means eliminated. `CloseQuestion` ends the game and sets `Game.EndReason` / `CloseQuestionResult.Reason` (`domain.GameOverReason`):
- `last_player_standing`: one active player left, they win
- `all_eliminated`: nobody left, no winner
- `no_more_questions`: last question played with 2+ survivors; the survivor with the most lives wins, a tie is a draw (empty winner). This draw rule is intended, keep it.

Question lifecycle: `Game.QuestionOpen` is set by `StartNextQuestion` and cleared on close. Start requires no open question (`ErrQuestionOpen`), close and answers require one (`ErrNoActiveQuestion`). This is what prevents a question from being closed twice.

Per-game config: `POST /games` may set `initial_lives` and `answer_timeout_seconds`, passed to `Manager.Create` as the engine's `EngineConfig` (`Engine.Config()`); `GameHandler.cfg` only holds the defaults. With a timeout, `StartNextQuestion` arms a `time.AfterFunc` under the lock that calls `closeQuestion(idx)`; that function checks and closes under one lock and refuses any other question, and every close stops the timer.

Persistence of a close lives in `GameHandler.persistClose`, registered with `Engine.OnQuestionClosed` at game creation, so manual and timeout closes are both persisted. Do not persist close results in the HTTP handler.

What a client receives about the open question is built in one place, `Engine.questionPayloadLocked` (never the answer): `question_started`, `Engine.CurrentQuestion` (adds `answered_by`, used by `GET /games/{id}` and `game_joined`). `closes_at` is `QuestionStart` + timeout. `question_closed` and the close response carry the scoreboard (`Engine.scoreboardLocked`, sorted by name), also used for `GET /games/{id}` players. `AddPlayer` broadcasts `player_joined`.

`SubmitAnswer` rejects unknown option IDs (`ErrInvalidOption`, the player may answer again). The WS message handler reports every rejected answer to that player only with `answer_rejected` and a code (`answerRejectCode`), through the local hub.

Clients must be told explicitly when questions run out: `question_started.is_last`, `question_closed.remaining_questions`, `game_over.reason`, and `StartNextQuestion` returns `ErrNoMoreQuestions` (HTTP 409, `code: no_more_questions`) on a game finished by `no_more_questions`. Handlers map engine errors to stable codes in `writeGameError`.

### Game access rules

- Each `domain.Player` records the `ActorID` (`auth.Actor.Sub`) that created it (`AddPlayer(id, name, actorID)`).
- The **host** is the actor that owns the game's owner player (`Engine.HostActorID()`). Only the host can `POST /games/{id}/start` and `/close` (403 otherwise).
- `/ws` requires the current actor to own `playerId` (`Engine.PlayerActorID()`), 403 otherwise.

### Eviction

`App.sweepGames` runs every minute and calls `Manager.Sweep`, which removes games for which `Engine.Expired` is true: finished games after `GAME_FINISHED_TTL` (default 10m), other games after `GAME_IDLE_TTL` (default 2h) without activity. For each removed game, `gameSessionStore.Remove` stops the Redis subscription and closes the WebSocket connections (`Hub.CloseAll`).

### Redis pub/sub broadcaster

`redis.PubSubBroadcaster` implements `game.Broadcaster` via Redis pub/sub:
- `Broadcast` / `BroadcastTo` → publish to channel `game:<id>:events`
- A subscriber goroutine reads from that channel → forwards to the local `ws.Hub` → WS clients

The broadcaster and hub are created per game in `app.gameSessionStore.GetOrCreate`, released by `Remove` on eviction, and all stopped during graceful shutdown.

The event path could fan out across instances, but game state is still held in the memory of the instance that created the game. Running several instances therefore requires routing every request of a game (HTTP and WS) to the same instance. Do not describe the backend as horizontally scalable.

**Note:** `game_joined` is sent directly via `hub.BroadcastTo` (bypassing Redis) because it is a connection handshake that must arrive immediately and synchronously before the read/write pumps start.

### Postgres persistence

Writes are best-effort (non-fatal: logged but don't abort requests):

| Operation               | Persisted fields                          |
|-------------------------|-------------------------------------------|
| `POST /games`           | game row + owner player row               |
| `POST /games/{id}/join` | player row                                |
| `POST /games/{id}/start`| `games.status = 'running'`                |
| `POST /games/{id}/close`| `players.lives` + `players.active` for every player who lost a life; `games.status = 'finished'` if game over |

The player's actor ID is not persisted.

### Authentication

Auth lives in `internal/auth/`. Two modes controlled by `OIDC_ENABLED`:

**Dev mode (`OIDC_ENABLED=false`, default):** `auth.Middleware` reads `X-Debug-Actor-Type` / `X-Debug-Actor-Id` request headers and populates the context with an `auth.Actor`. Since browsers cannot set headers on a WebSocket handshake, the `debugActorType` / `debugActorId` query parameters are accepted as a fallback (headers win). No session required.

**OIDC mode (`OIDC_ENABLED=true`):** Standard Authorization Code Flow.
1. `GET /auth/login`: generates `state`+`nonce`, stores them in a signed JWT cookie (`oauth2_state`), redirects to the OIDC provider.
2. `GET /auth/callback`: verifies state cookie, exchanges code for ID token, validates nonce, issues a signed session JWT as `quizz_session` cookie (HttpOnly, 24h TTL), redirects to `OIDC_FRONTEND_URL`.
3. `auth.Middleware` validates the `quizz_session` cookie on every protected request and sets the actor in context.
4. `POST /auth/logout`: clears the cookie.

Role mapping: the claim named `OIDC_ROLE_CLAIM` (default: `role`) is checked; if it equals `OIDC_ADMIN_ROLE` (default: `admin`), the actor gets `ActorTypeAdmin`, otherwise `ActorTypeUser`. Both scalar string and string array claim values are handled.

All routes except `/health`, `/auth/login`, `/auth/callback`, and `/auth/logout` are protected by `auth.Middleware`. `corsMiddleware` (`internal/app/cors.go`) runs before it so preflights, which carry no cookie, are answered; it is a no-op when `CORS_ALLOWED_ORIGINS` is empty.

`GET /games` lists the in-memory games where the caller is host or owns a player; `GET /games/{id}` adds `me` (`Engine.ActorView`). Both let a client recover its player after losing local state.

`extractActor` in `internal/handler/actor.go` reads the actor from the context set by the middleware. It is the only place handlers access identity.

Docker Compose only passes the variables listed under `api.environment` in `docker-compose.yml`. A new config variable must be added there too, or it will be ignored under `make up`.

### Question lists and access rules

`QuestionListStore` (implemented by `postgres.DB`) manages the catalog:
- Public lists: created by `admin` actors, readable by all
- Private lists: created by `user` actors, visible only to the owning actor

Always use `canReadList` / `canEditList` (`internal/handler/actor.go`) for these rules; do not re-implement them inline. `correct_option_id` is only returned to actors who can edit the list (`ListQuestions` blanks it, the JSON tag is `omitempty`).

Editors can also rename and delete lists (questions and custom themes cascade; `games.question_list_id` is `ON DELETE SET NULL` since migration 004), delete questions (the store renumbers `order_index`) and reorder them (`PUT .../questions/order` with every question ID exactly once).

### Themes

`ThemeStore` (implemented in `internal/postgres/themes.go`) and `handler.ThemeHandler` serve `/themes` (global, admin-managed) and `/question-lists/{id}/themes` (custom, managed by list editors). Each route only sees themes of its own scope (404 otherwise). A question's `theme_id` must be global or belong to the question's list: `QuestionListHandler.checkTheme` enforces it on create and update (`theme_not_found`, `theme_from_another_list`). Names are unique per scope, case-insensitive (partial unique indexes, mapped to `store.ErrConflict` → 409 `theme_name_taken`). Deleting a theme sets `theme_id` to NULL on its questions (FK `ON DELETE SET NULL`).

Postgres errors are translated by `mapError` into `store.ErrNotFound` / `store.ErrConflict`; handlers switch on those, never on driver errors.

Handler tests use `memStore` (`internal/handler/memstore_test.go`), an in-memory store mirroring these constraints, behind a real chi router and the dev auth middleware (`catalogServer`).

### WebSocket lifecycle

Each game has one `ws.Hub` (held in `app.gameSessionStore`). When a player connects to `/ws?gameId=&playerId=`:
1. The upgrader checks the `Origin` against `CORS_ALLOWED_ORIGINS` when it is set (`GameHandler.RestrictOrigins`), then the handler looks up the engine and checks that the player exists and belongs to the current actor.
2. A `ws.Client` is created (buffered send channel, 256 msgs) and registered in the hub. One connection per player: `Hub.Register` closes the connection it replaces, and `Hub.Unregister(playerID, client)` only removes that exact client, so a replaced connection shutting down never unregisters the new one.
3. `WritePump` and `ReadPump` run in separate goroutines.
4. `game_joined` is sent to the player immediately, with its lives and the open question (so a reconnecting player can answer).
5. Incoming client messages (`submit_answer`) are routed back to the engine.

### Migrations

SQL is embedded in the binary via `//go:embed` in `migrations/migrations.go`. Four files are concatenated: `001_initial.sql` (base tables), `002_question_lists.sql` (catalog tables + `question_list_id` column on `games`), `003_themes.sql` (`themes` table + `theme_id` on questions) and `004_catalog_deletes.sql` (`games.question_list_id` becomes `ON DELETE SET NULL`, in a guarded `DO` block). Statements that cannot use `IF NOT EXISTS` must be wrapped in a `DO` block that checks the catalog first. `app.New()` calls `pg.RunMigrations()` on every startup, which is safe because all statements use `IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`.

To add a migration: create `00N_name.sql`, embed it in `migrations.go`, and append it to `SQL`.

### Test-UI (tools/test-ui)

Vite + Vue 3 + TypeScript dev tool, not production code. All HTTP calls go through Vite's proxy (`/api/*` → backend, `/ws` → backend WS) so there are no CORS issues. `VITE_API_TARGET` controls the proxy target.

The `actor` reactive state (`src/actor.ts`) holds the debug identity (injected as `X-Debug-Actor-*` headers in dev mode). `PlayerCard.vue` remembers the actor used at join time and passes it as `debugActorType` / `debugActorId` on the WS URL. `fetchSession()` calls `GET /auth/me` on mount and populates `sessionUser`. `ActorBar.vue` shows OIDC user info + logout when `oidc_enabled: true`, or the debug controls when `oidc_enabled: false`.

`ThemesPanel.vue` manages one theme scope (global without `listId`, a list's custom themes with it) and is used in the Themes tab and under the selected list. Global themes live in the shared `src/themes.ts` store so the question form sees changes made in the Themes tab. `api.ts` does not parse the body of `204` responses.

## Key design constraints

- `game.Engine` has no knowledge of transport (HTTP/WS) or storage: it takes a `Broadcaster` interface.
- `game.Broadcaster` is the only coupling point between game logic and delivery. `redis.PubSubBroadcaster` implements it in the app; `ws.Hub` implements it too and is what the Redis subscriber forwards to.
- `store.GameStore` / `PlayerStore` / `QuestionListStore` / `ThemeStore` are interfaces; `postgres.DB` implements all four. `GameStore` and `PlayerStore` may be `nil` in handler tests.
- `game.Engine.AddQuestion` is kept for test convenience only; HTTP no longer exposes it. Production games get their questions from the list at creation time.
- The `go build` in `.air.toml` uses `-buildvcs=false`, required because the container can't access git metadata.

## Documentation

- Do not use em dashes in documentation.
- `docs/frontend-integration.md` is the contract for UI developers (endpoints, payloads, events, error codes, TypeScript types). Update it with any change to the HTTP API or the WebSocket protocol.
