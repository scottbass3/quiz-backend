# CLAUDE.md

## Commands

Common commands are listed in the Makefile (`make test` runs `go test ./... -race -count=1`).

The test-ui (Vite dev server) is at **http://localhost:5173**.
The API is at **http://localhost:8080** (overridable via `HTTP_PORT` in `.env`).
`make up-multi` also starts a second API instance, `api2`, on **http://localhost:8081** (`HTTP_PORT_2`); `make load-test-multi` plays one game across both.

## Architecture

### Request flow

```
HTTP/WS request (any instance)
  → chi router + corsMiddleware + auth.Middleware (internal/app/, internal/auth/)
  → handler (internal/handler/game.go or question_list.go)
  → game.Engine (internal/game/engine.go)             ← game rules, no state of its own
  → game.StateStore = redis.GameStateStore            ← all game state, shared by every instance
  → redis.GamePublisher (internal/redis/broadcaster)  ← publishes events to game:<id>:events
  → redis.Subscribe → ws.Hub (per instance)           ← fan-out to this instance's WS clients
  → postgres.DB (internal/postgres/)                  ← history, best-effort, non-fatal
```

### Catalog vs runtime separation

**Catalog** (persisted in Postgres):
- `question_lists` table: name, visibility, owner
- `question_list_questions` table: ordered questions per list, optional `theme_id`
- `themes` table: `question_list_id IS NULL` is a global theme, otherwise a custom theme of that list (exposed as `scope`: `global` / `list`)

**Runtime** (in Redis):
- A game copies the questions of its list (with the answers) into Redis when `POST /games` is called; it never reads Postgres again.
- Game state survives Postgres outages and API restarts; Redis is persisted with its append-only file.

### Game state model

The `game` package holds the **rules** only. `game.Engine` is a handle on one game (ID + manager); every operation reads and writes a `game.StateStore` (`internal/game/store.go`), implemented by `redis.GameStateStore` (`internal/redis/gamestate.go`). Any instance can therefore serve any request of any game; there is no sticky routing and no in-memory registry.

Redis layout (keys of a game share the `{id}` hash tag, so they sit in one Cluster slot):

| Key                         | Type   | Content                                                    |
|-----------------------------|--------|------------------------------------------------------------|
| `game:{id}:meta`            | hash   | status, owner, config, `current_q_idx`, `question_open`, `question_start`, `end_reason` |
| `game:{id}:questions`       | hash   | index → question JSON (with the correct answer)            |
| `game:{id}:players`         | hash   | player ID → JSON (name, lives, active, actor)              |
| `game:{id}:answers:<idx>`   | hash   | player ID → option ID                                      |
| `game:{id}:lock`            | string | per-game lock token                                        |
| `actor:{actor}:games`       | zset   | game IDs of an actor, by creation time (`GET /games`)      |
| `games:deadlines`           | zset   | `gameID|idx` by answer deadline (ms)                       |

Concurrency rules (keep them when touching the engine or the store):
- **Transitions** (join, start, close) run under `StateStore.Lock` (`SET NX PX`, 10s expiry, `ErrBusy` → HTTP 503 `game_busy` after 5s of waiting). `Engine.locked` loads the state under the lock.
- **Answers** never take the lock: `RecordAnswer` is a Lua script that checks `status`, `question_open` and `current_q_idx` then `HSETNX`es the answer.
- **Close** calls `CloseAnswers`, a Lua script that flips `question_open` to 0 and returns the answers in one step, so an answer is either counted or rejected (`no_active_question`), never lost after being accepted.
- **Events are broadcast after the transition is stored and the lock is released.**
- Every write refreshes TTLs: `GAME_IDLE_TTL` while unfinished, `GAME_FINISHED_TTL` once finished (answers keys included). Expiry replaces any sweeper; `ErrGameNotFound` means "unknown or expired".

`game.State` (`internal/game/state.go`) is the loaded view: players, config, progress, the current question and its answers. Its methods (`HostActorID`, `PlayerActorID`, `ActorView`, `Scoreboard`, `CurrentQuestion`, `RemainingQuestions`) are pure; handlers load the state once per request.

Game rules: a wrong or missing answer costs one life, 0 lives means eliminated (`applyPenalties`). The game ends and sets `State.EndReason` / `CloseQuestionResult.Reason` (`domain.GameOverReason`):
- `last_player_standing`: one active player left, they win
- `all_eliminated`: nobody left, no winner
- `no_more_questions`: last question played with 2+ survivors; the survivor with the most lives wins, a tie is a draw (empty winner). This draw rule is intended, keep it.

Question lifecycle: `question_open` is set by `StartNextQuestion` and cleared on close. Start requires no open question (`ErrQuestionOpen`), close and answers require one (`ErrNoActiveQuestion`).

Per-game config: `POST /games` may set `initial_lives` and `answer_timeout_seconds`, stored in the game's meta (`State.Config`); `GameHandler.cfg` only holds the defaults.

**Answer deadlines:** with a timeout, `StartNextQuestion` adds `gameID|idx` to `games:deadlines`. Every instance runs `Manager.RunDeadlines` (every 250 ms), which calls `CloseDue`: a Lua script claims the due entries (range + remove in one step, so each deadline goes to one instance), then `closeQuestion(ctx, idx)` closes only if `idx` is still the open question. A manual close removes the deadline. Tests call `CloseDue(ctx, someTime)` directly instead of sleeping.

Persistence of a close lives in `GameHandler.persistClose`, registered with `Manager.OnQuestionClosed` by `NewGameHandler`, so closes by the host and by deadlines (on any instance) are persisted. Do not persist close results in the HTTP handler.

What a client receives about the open question is built in one place, `State.questionPayload` (never the answer): `question_started`, and `State.CurrentQuestion` (adds `answered_by`, used by `GET /games/{id}` and `game_joined`). `closes_at` is `QuestionStart` + timeout. `question_closed` and the close response carry the scoreboard (`State.Scoreboard`, sorted by name), also used for `GET /games/{id}` players. `AddPlayer` broadcasts `player_joined`.

`SubmitAnswer` rejects unknown option IDs (`ErrInvalidOption`, the player may answer again). The WS message handler reports every rejected answer to that player only with `answer_rejected` and a code (`answerRejectCode`), through the local hub.

Clients must be told explicitly when questions run out: `question_started.is_last`, `question_closed.remaining_questions`, `game_over.reason`, and `StartNextQuestion` returns `ErrNoMoreQuestions` (HTTP 409, `code: no_more_questions`) on a game finished by `no_more_questions`. Handlers map engine errors in `writeGameError` (409 codes) and `writeEngineError` (404, 503 `game_busy`, 500).

### Game access rules

- Each `domain.Player` records the `ActorID` (`auth.Actor.Sub`) that created it (`AddPlayer(ctx, id, name, actorID)`).
- The **host** is the actor that owns the game's owner player (`State.HostActorID()`). Only the host can `POST /games/{id}/start` and `/close` (403 otherwise).
- `/ws` requires the current actor to own `playerId` (`State.PlayerActorID()`), 403 otherwise.

### Events across instances

`redis.Publisher` (`For(gameID)` returns a `game.Broadcaster`) publishes `{t: target, e: event}` on `game:<id>:events` from any instance; it holds no subscription.

`app.gameSessionStore` keeps, per instance, a local `ws.Hub` and a Redis subscription (`redis.Subscribe`, which waits for Redis to confirm) for each game that has WebSocket clients **on this instance**. `Acquire` creates them on the first client, `Release` drops them with the last one (reference counted). `App.sweepSessions` closes the local connections of games that no longer exist in Redis.

**Note:** `game_joined` and `answer_rejected` are written directly to the local hub (bypassing Redis): the first is a handshake that must arrive before the pumps start, the second only concerns a connection of this instance.

### Postgres persistence

Writes are best-effort (non-fatal: logged but don't abort requests). Postgres is history only; game state is never read back from it.

| Operation               | Persisted fields                          |
|-------------------------|-------------------------------------------|
| `POST /games`           | game row + owner player row               |
| `POST /games/{id}/join` | player row                                |
| `POST /games/{id}/start`| `games.status = 'running'`                |
| question close (host or deadline) | `players.lives` + `players.active` for every player who lost a life; `games.status = 'finished'` if game over |

The player's actor ID is not persisted.

### Authentication

Auth lives in `internal/auth/`. Two modes controlled by `OIDC_ENABLED`:

**Dev mode (`OIDC_ENABLED=false`, default):** `auth.Middleware` reads `X-Debug-Actor-Type` / `X-Debug-Actor-Id` request headers and populates the context with an `auth.Actor`. Since browsers cannot set headers on a WebSocket handshake, the `debugActorType` / `debugActorId` query parameters are accepted as a fallback (headers win). No session required.

**OIDC mode (`OIDC_ENABLED=true`):** Standard Authorization Code Flow.
1. `GET /auth/login`: generates `state`+`nonce`, stores them in a signed JWT cookie (`oauth2_state`), redirects to the OIDC provider.
2. `GET /auth/callback`: verifies state cookie, exchanges code for ID token, validates nonce, issues a signed session JWT as `quizz_session` cookie (HttpOnly, 24h TTL), redirects to `OIDC_FRONTEND_URL`.
3. `auth.Middleware` validates the `quizz_session` cookie on every protected request and sets the actor in context.
4. `POST /auth/logout`: clears the cookie.

Sessions are stateless signed JWTs, so they work on every instance as long as they share `SESSION_SECRET`.

Role mapping: the claim named `OIDC_ROLE_CLAIM` (default: `role`) is checked; if it equals `OIDC_ADMIN_ROLE` (default: `admin`), the actor gets `ActorTypeAdmin`, otherwise `ActorTypeUser`. Both scalar string and string array claim values are handled.

All routes except `/health`, `/auth/login`, `/auth/callback`, and `/auth/logout` are protected by `auth.Middleware`. `corsMiddleware` (`internal/app/cors.go`) runs before it so preflights, which carry no cookie, are answered; it is a no-op when `CORS_ALLOWED_ORIGINS` is empty.

`GET /games` lists the games where the caller is host or owns a player (from the `actor:{id}:games` index); `GET /games/{id}` adds `me` (`State.ActorView`). Both let a client recover its player after losing local state.

`extractActor` in `internal/handler/actor.go` reads the actor from the context set by the middleware. It is the only place handlers access identity.

Docker Compose only passes the variables listed under `api.environment` in `docker-compose.yml` (`api2` reuses them through a YAML anchor). A new config variable must be added there too, or it will be ignored under `make up`.

### Question lists and access rules

`QuestionListStore` (implemented by `postgres.DB`) manages the catalog:
- Public lists: created by `admin` actors, readable by all
- Private lists: created by `user` actors, visible only to the owning actor

Always use `canReadList` / `canEditList` (`internal/handler/actor.go`) for these rules; do not re-implement them inline. `correct_option_id` is only returned to actors who can edit the list (`ListQuestions` blanks it, the JSON tag is `omitempty`).

Editors can also rename and delete lists (questions and custom themes cascade; `games.question_list_id` is `ON DELETE SET NULL` since migration 004), delete questions (the store renumbers `order_index`) and reorder them (`PUT .../questions/order` with every question ID exactly once).

### Themes

`ThemeStore` (implemented in `internal/postgres/themes.go`) and `handler.ThemeHandler` serve `/themes` (global, admin-managed) and `/question-lists/{id}/themes` (custom, managed by list editors). Each route only sees themes of its own scope (404 otherwise). A question's `theme_id` must be global or belong to the question's list: `QuestionListHandler.checkTheme` enforces it on create and update (`theme_not_found`, `theme_from_another_list`). Names are unique per scope, case-insensitive (partial unique indexes, mapped to `store.ErrConflict` → 409 `theme_name_taken`). Deleting a theme sets `theme_id` to NULL on its questions (FK `ON DELETE SET NULL`).

Postgres errors are translated by `mapError` into `store.ErrNotFound` / `store.ErrConflict`; handlers switch on those, never on driver errors.

### WebSocket lifecycle

When a player connects to `/ws?gameId=&playerId=` (on any instance):
1. The upgrader checks the `Origin` against `CORS_ALLOWED_ORIGINS` when it is set (`GameHandler.RestrictOrigins`), then the handler loads the state and checks that the player exists and belongs to the current actor.
2. `gameSessionStore.Acquire` returns this instance's hub for the game (subscribing to Redis if needed); the handler `Release`s it when the connection ends.
3. A `ws.Client` is created (buffered send channel, 256 msgs) and registered in the hub. One connection per player per hub: `Hub.Register` closes the connection it replaces, and `Hub.Unregister(playerID, client)` only removes that exact client. (A player connected to two instances at once keeps both connections.)
4. `WritePump` and `ReadPump` run in separate goroutines.
5. `game_joined` is sent to the player immediately, with its lives and the open question (so a reconnecting player can answer).
6. Incoming client messages (`submit_answer`) are routed back to the engine.

### Migrations

SQL is embedded in the binary via `//go:embed` in `migrations/migrations.go`. Four files are concatenated: `001_initial.sql` (base tables), `002_question_lists.sql` (catalog tables + `question_list_id` column on `games`), `003_themes.sql` (`themes` table + `theme_id` on questions) and `004_catalog_deletes.sql` (`games.question_list_id` becomes `ON DELETE SET NULL`, in a guarded `DO` block). Statements that cannot use `IF NOT EXISTS` must be wrapped in a `DO` block that checks the catalog first. `app.New()` calls `pg.RunMigrations()` on every startup under a Postgres advisory lock, so instances starting together run them one at a time.

To add a migration: create `00N_name.sql`, embed it in `migrations.go`, and append it to `SQL`.

### Tests

- Game and handler tests run against **miniredis** (in-process Redis with Lua support): `newCluster` / `instance()` in `internal/game/engine_test.go` (several managers over one Redis = several instances), `testManager` in `internal/handler/manager_test.go`. Use `mr.FastForward` for expiry and `Manager.CloseDue` for deadlines instead of sleeping.
- Catalog handler tests use `memStore` (`internal/handler/memstore_test.go`), an in-memory store mirroring the Postgres constraints, behind a real chi router and the dev auth middleware (`catalogServer`).
- There is no Postgres test database: SQL changes are checked against the Docker stack.

### Test-UI (tools/test-ui)

Vite + Vue 3 + TypeScript dev tool, not production code. All HTTP calls go through Vite's proxy (`/api/*` → backend, `/ws` → backend WS) so there are no CORS issues. `VITE_API_TARGET` controls the proxy target.

The `actor` reactive state (`src/actor.ts`) holds the debug identity (injected as `X-Debug-Actor-*` headers in dev mode). `PlayerCard.vue` remembers the actor used at join time and passes it as `debugActorType` / `debugActorId` on the WS URL. `fetchSession()` calls `GET /auth/me` on mount and populates `sessionUser`. `ActorBar.vue` shows OIDC user info + logout when `oidc_enabled: true`, or the debug controls when `oidc_enabled: false`.

`ThemesPanel.vue` manages one theme scope (global without `listId`, a list's custom themes with it) and is used in the Themes tab and under the selected list. Global themes live in the shared `src/themes.ts` store so the question form sees changes made in the Themes tab. `api.ts` does not parse the body of `204` responses.

## Key design constraints

- `game.Engine` has no knowledge of transport (HTTP/WS) or storage technology: it uses the `game.Broadcaster` and `game.StateStore` interfaces. Redis specifics (keys, scripts, TTLs) stay in `internal/redis`.
- `game.Broadcaster` is the only coupling point between game logic and delivery. `redis.GamePublisher` implements it in the app; `ws.Hub` implements it too and is what the Redis subscriber forwards to.
- `store.GameStore` / `PlayerStore` / `QuestionListStore` / `ThemeStore` are interfaces; `postgres.DB` implements all four. `GameStore` and `PlayerStore` may be `nil` in handler tests.
- Never keep game state in instance memory: an instance may die or another may serve the next request.
- The `go build` in `.air.toml` uses `-buildvcs=false`, required because the container can't access git metadata.

## Documentation

- Do not use em dashes in documentation.
- `docs/frontend-integration.md` is the contract for UI developers (endpoints, payloads, events, error codes, TypeScript types). Update it with any change to the HTTP API or the WebSocket protocol.
