# quizz-backend

Real-time multiplayer quiz backend in Go, inspired by Master of the Grid by Elisee.

Players join a game, answer multiple-choice questions over WebSocket and lose a life for every wrong or missing answer. The last player standing wins.

## Contents

- [Game rules](#game-rules)
- [Architecture](#architecture)
- [Getting started](#getting-started)
- [Configuration](#configuration)
- [Authentication](#authentication)
- [HTTP API](#http-api) (including [themes](#themes))
- [WebSocket](#websocket)
- [Test UI](#test-ui)
- [Test scenarios](#test-scenarios)
- [Tests and load tests](#tests-and-load-tests)
- [Useful commands](#useful-commands)
- [Known limitations](#known-limitations)

## Game rules

- A game is created from a **question list**. Its questions are copied into memory when the game is created and played in order.
- The actor who creates the game is its **host**. The host is also added as the first player (the "owner" player).
- Other players join while the game is still `waiting`. Joining is closed once the first question starts.
- Every player starts with the same number of lives: `initial_lives` if given at game creation, `GAME_INITIAL_LIVES` otherwise (default 3).
- The host starts a question, players answer over WebSocket (first answer only), then the question is closed: by the host, or automatically after `answer_timeout_seconds` if the game was created with one. Only one question is open at a time: the host must close it before starting the next.
- On close, every active player who answered wrong **or did not answer** loses one life. A player at 0 lives is eliminated.
- The game ends when a question is closed and one of these holds. The reason is sent to clients in `game_over`:

  | Reason                 | When                                                | Winner                                        |
  |------------------------|-----------------------------------------------------|-----------------------------------------------|
  | `last_player_standing` | exactly one active player is left                   | that player                                   |
  | `all_eliminated`       | every remaining player lost their last life at once | none                                          |
  | `no_more_questions`    | the last question was played, 2+ players survive    | the survivor with the most lives, none on a tie (draw) |

Game status goes `waiting` → `running` (first question started) → `finished`.

Clients know in advance when the list runs out: `question_started` carries `is_last`, and `question_closed` carries `remaining_questions`.

## Architecture

```
cmd/api          entry point: loads config, wires the app, handles OS signals
internal/
  config/        env-driven configuration
  domain/        pure data types: Game, Player, Question, QuestionList, Answer, Event
  game/          game engine (thread-safe, in-memory) and the manager that holds all engines
  store/         repository interfaces (GameStore, PlayerStore, QuestionListStore)
  postgres/      pgx/v5 implementation of the store interfaces
  redis/         Redis client and the pub/sub event broadcaster
  ws/            per-game Hub (fan-out) and Client (read/write pumps per connection)
  auth/          auth middleware, OIDC provider, session and state JWTs
  handler/       chi HTTP handlers and the WebSocket upgrade
  app/           wires all layers, runs the HTTP server and the game sweeper
migrations/      SQL embedded in the binary, applied automatically at startup
k6/              load test scripts
tools/test-ui/   Vite + Vue 3 dev tool for manual testing (not production code)
```

### Request flow

```
HTTP / WS request
  → chi router, auth middleware        (internal/app, internal/auth)
  → handler                            (internal/handler)
  → game.Engine                        (internal/game)      all runtime game state lives here
  → redis.PubSubBroadcaster            (internal/redis)     publishes events to game:<id>:events
  → ws.Hub                             (internal/ws)        forwards events to connected clients
  → postgres.DB                        (internal/postgres)  best-effort persistence
```

### Key design decisions

- **Catalog vs runtime.** Question lists, their questions and themes live in Postgres. A game copies the questions of its list (with their theme) once, at creation, and never reads Postgres again for them.
- **In-memory game state.** Each game is a `game.Engine` guarded by a `sync.RWMutex`. Events are broadcast *after* the lock is released to avoid contention with the hub.
- **Transport-agnostic engine.** The engine only knows a `Broadcaster` interface. In the app it is backed by Redis pub/sub: the engine publishes to `game:<id>:events`, a subscriber goroutine forwards each event to the local `ws.Hub`, which writes to the WebSocket clients.
- **One hub per game.** Each game has its own hub and Redis subscription, created with the game and released when the game is evicted.
- **Best-effort persistence.** Postgres writes (game, players, lives, status) are logged on failure but never abort a request. Answers are not persisted.
- **Eviction.** A sweeper runs every minute and removes finished games after `GAME_FINISHED_TTL` and unfinished games idle for `GAME_IDLE_TTL`, closing their WebSocket connections and Redis subscription.
- **Embedded migrations.** SQL files are embedded with `//go:embed` and applied on every startup. Every statement is idempotent (`IF NOT EXISTS`), so no external migration tool is needed.
- **Libraries.** `chi` for routing, `gorilla/websocket` for WebSocket (explicit pumps with ping/pong), `pgx/v5`, `go-redis/v9`, `coreos/go-oidc` and `golang-jwt`.

## Getting started

### Prerequisites

- Docker and Docker Compose v2
- Go 1.23+ (to run or test outside Docker; `make test` also needs a C compiler because it enables the race detector)
- Node 18+ (only to run the test UI outside Docker)
- [k6](https://k6.io/) (only for load tests)

### With Docker Compose

```bash
cp .env.example .env   # optional, every variable has a default
make up                # builds and starts api, postgres, redis and the test UI
```

- API: `http://localhost:8080`
- Test UI: `http://localhost:5173`

The API runs with [air](https://github.com/air-verse/air) and rebuilds on every `.go` change. Migrations run automatically when the API starts.

### Without Docker (API only)

The API reads its configuration from the process environment only, it does not load `.env` by itself.

```bash
docker compose up -d postgres redis
set -a && . ./.env && set +a
go run ./cmd/api
```

## Configuration

### Read by the API

| Variable             | Default                                             | Description                                                |
|----------------------|-----------------------------------------------------|------------------------------------------------------------|
| `HTTP_ADDR`          | `:8080`                                             | Address the server listens on                              |
| `DATABASE_URL`       | `postgres://quizz:quizz@localhost:5432/quizz?...`   | PostgreSQL DSN                                             |
| `REDIS_ADDR`         | `localhost:6379`                                    | Redis address                                              |
| `REDIS_PASSWORD`     | *(empty)*                                           | Redis password                                             |
| `LOG_LEVEL`          | `info`                                              | `debug`, `info`, `warn` or `error`                         |
| `GAME_INITIAL_LIVES` | `3`                                                 | Lives per player                                           |
| `GAME_FINISHED_TTL`  | `10m`                                               | How long a finished game stays in memory                   |
| `GAME_IDLE_TTL`      | `2h`                                                | Evict unfinished games with no activity for this long      |
| `SHUTDOWN_TIMEOUT`   | `10s`                                               | Graceful shutdown window                                   |
| `SESSION_SECRET`     | `dev-secret-change-in-production`                   | HMAC key for session and OAuth2 state JWTs                 |
| `OIDC_ENABLED`       | `false`                                             | `true` to use OIDC instead of the debug headers            |
| `OIDC_ISSUER_URL`    | *(empty)*                                           | OIDC issuer (used for discovery)                           |
| `OIDC_CLIENT_ID`     | *(empty)*                                           | OAuth2 client ID                                           |
| `OIDC_CLIENT_SECRET` | *(empty)*                                           | OAuth2 client secret                                       |
| `OIDC_REDIRECT_URL`  | `http://localhost:8080/auth/callback`               | Callback URL registered at the provider (points to the API) |
| `OIDC_FRONTEND_URL`  | `http://localhost:5173`                             | Where the browser is sent after a successful login         |
| `OIDC_ROLE_CLAIM`    | `role`                                              | ID token claim holding the role (string or string array)   |
| `OIDC_ADMIN_ROLE`    | `admin`                                             | Role value that maps to the `admin` actor type             |

Durations use Go syntax (`90s`, `10m`, `2h`). Invalid values fall back to the default.

### Used by Docker Compose only

| Variable            | Default | Description                     |
|---------------------|---------|---------------------------------|
| `HTTP_PORT`         | `8080`  | Host port mapped to the API     |
| `UI_PORT`           | `5173`  | Host port mapped to the test UI |
| `POSTGRES_USER`     | `quizz` | Postgres user                   |
| `POSTGRES_PASSWORD` | `quizz` | Postgres password               |
| `POSTGRES_DB`       | `quizz` | Postgres database               |
| `POSTGRES_PORT`     | `5432`  | Host port mapped to Postgres    |
| `REDIS_PORT`        | `6379`  | Host port mapped to Redis       |

Under Compose, `HTTP_ADDR`, `DATABASE_URL` and `REDIS_ADDR` are set by `docker-compose.yml` to reach the other containers. The values in `.env` for these three only apply to local runs.

## Authentication

Every route except `/health`, `/auth/login`, `/auth/callback` and `/auth/logout` goes through the auth middleware, which resolves an **actor**: an ID and a type, `admin` or `user`. There are two modes, selected by `OIDC_ENABLED`.

### Dev mode (`OIDC_ENABLED=false`, default)

The actor is read from two headers:

| Header               | Values          | Default     |
|----------------------|-----------------|-------------|
| `X-Debug-Actor-Type` | `admin`, `user` | `user`      |
| `X-Debug-Actor-Id`   | any string      | `anonymous` |

Browsers cannot set headers on a WebSocket handshake, so the same values are also accepted as the `debugActorType` and `debugActorId` query parameters. Headers take precedence.

**Dev mode trusts the client completely. Never expose it publicly.**

### OIDC mode (`OIDC_ENABLED=true`)

Standard Authorization Code flow:

1. `GET /auth/login` generates a `state` and a `nonce`, stores them in a signed, short-lived `oauth2_state` cookie and redirects to the provider.
2. `GET /auth/callback` checks the state, exchanges the code, verifies the ID token and its nonce, then sets the `quizz_session` cookie (signed JWT, HttpOnly, 24h) and redirects to `OIDC_FRONTEND_URL`.
3. The middleware validates `quizz_session` on every protected request. A missing or invalid cookie gives `401`.
4. `POST /auth/logout` clears the cookie.

The actor ID is the token `sub`. The actor type is `admin` when the `OIDC_ROLE_CLAIM` claim equals (or, for an array, contains) `OIDC_ADMIN_ROLE`, and `user` otherwise.

Register `OIDC_REDIRECT_URL` as an allowed redirect URI at your provider, and set a random `SESSION_SECRET`.

## HTTP API

All bodies are JSON. Errors are returned as `{ "error": "..." }`. Game state errors on start and close, and the empty list error on game creation, also carry a stable `code` to switch on: `{ "error": "no more questions", "code": "no_more_questions" }`.

### Health

```
GET /health
→ 200 { "status": "ok", "uptime": "1m23s" }
```

### Auth

| Route                | Description                                                                 |
|----------------------|-----------------------------------------------------------------------------|
| `GET /auth/login`    | Redirects to the OIDC provider (`404` in dev mode)                          |
| `GET /auth/callback` | OIDC redirect target (`404` in dev mode)                                    |
| `POST /auth/logout`  | Clears the session cookie                                                   |
| `GET /auth/me`       | Current actor: `{ sub, name, email, actor_type, oidc_enabled }`, works in both modes |

### Question lists

| Visibility | Created by            | Readable by | Edited by (questions, custom themes) |
|------------|-----------------------|-------------|--------------------------------------|
| `public`   | `admin` actors only   | everyone    | any `admin` actor                    |
| `private`  | `user` actors only    | its owner   | its owner                            |

```
POST /question-lists
{ "name": "General culture", "description": "...", "visibility": "public" }
→ 201 { "id", "name", "description", "visibility", "owner_type", "owner_id", "created_at", "updated_at" }
  400 missing name or invalid visibility
  403 visibility not allowed for this actor type
```

```
GET /question-lists/public
→ 200 [ { ...list }, ... ]
```

```
GET /question-lists/private
→ 200 lists owned by the current actor
  403 actor is not a user
```

```
GET /question-lists/{id}
→ 200 list metadata
  403 private list owned by someone else
  404 unknown list
```

```
GET /question-lists/{id}/questions[?theme_id=<id>|none]
→ 200 [ { "id", "question_list_id", "text", "options", "correct_option_id", "order_index",
          "theme": { "id", "name", "scope" } | null }, ... ]
  403 private list owned by someone else
  404 unknown list
```

Questions are ordered by `order_index`. `theme_id=<id>` keeps the questions of one theme, `theme_id=none` the questions without theme.

```
POST /question-lists/{id}/questions
{
  "text": "Capital of France?",
  "options": [{"id":"a","text":"London"},{"id":"b","text":"Paris"},{"id":"c","text":"Berlin"}],
  "correct_option_id": "b",
  "theme_id": "..."
}
→ 201 { "question_id": "..." }
  400 missing text, fewer than 2 options or missing correct_option_id
  400 correct_option_id matches no option, or duplicate option ids
  400 code=theme_not_found / code=theme_from_another_list
  403 not allowed to edit this list
```

```
PUT /question-lists/{id}/questions/{questionID}
(same body as POST)
→ 200 the updated question, as returned by GET .../questions
  400 same validation as POST
  403 not allowed to edit this list
  404 unknown list, or question not in this list
```

Option IDs are generated when left empty. New questions are appended at the end of the list; `PUT` replaces the text, options, correct option and theme but keeps the position. `theme_id` is optional on both: omit it (or send `null` or `""`) for a question without theme, which also removes the theme on `PUT`.

### Themes

Themes categorize questions. They come in two scopes, returned as `scope`:

| Scope    | Route                                  | Readable by              | Managed by        |
|----------|----------------------------------------|--------------------------|-------------------|
| `global` | `/themes`                              | everyone                 | `admin` actors    |
| `list`   | `/question-lists/{id}/themes`          | readers of that list     | editors of that list |

A question references at most one theme, which must be a global theme or a custom theme of **its own** list. Names are trimmed, required, at most 100 characters and unique per scope, ignoring case: two lists may both have a "History" theme, and so may a list and the global scope.

Both routes offer the same operations:

```
GET    /themes                          GET    /question-lists/{id}/themes
POST   /themes                          POST   /question-lists/{id}/themes
GET    /themes/{themeID}                GET    /question-lists/{id}/themes/{themeID}
PUT    /themes/{themeID}                PUT    /question-lists/{id}/themes/{themeID}
DELETE /themes/{themeID}                DELETE /question-lists/{id}/themes/{themeID}
```

```
POST / PUT body:  { "name": "Geography", "description": "optional" }
Theme:            { "id", "scope", "question_list_id" (list scope only), "name", "description", "created_at", "updated_at" }

GET list   → 200 [ ...themes ] sorted by name
POST       → 201 theme
GET / PUT  → 200 theme
DELETE     → 204
  400 missing or too long name
  403 not allowed to read or manage this scope
  404 unknown theme, or theme of another scope (a list theme is not reachable through /themes or another list)
  409 code=theme_name_taken
```

Deleting a theme never fails because of questions: they are simply left without theme. Deleting a list is not supported, but would delete its custom themes.

### Games

```
POST /games
{ "owner_name": "Alice", "question_list_id": "...", "initial_lives": 3, "answer_timeout_seconds": 20 }
→ 201 { "game_id", "owner_id", "question_list_id", "total_questions" }
  400 missing owner_name or question_list_id
  400 initial_lives below 1, or negative answer_timeout_seconds
  400 code=empty_question_list: the list has no questions
  403 private list owned by someone else
  404 unknown list
```

The current actor becomes the host, and an owner player named `owner_name` is created for them (`owner_id` is that player's ID).

`initial_lives` and `answer_timeout_seconds` are optional. Without `initial_lives` the game uses `GAME_INITIAL_LIVES`. Without `answer_timeout_seconds`, or with `0`, questions stay open until the host closes them; otherwise each question is closed automatically after that many seconds, with the same effects and events as a manual close (including persistence).

```
POST /games/{id}/join
{ "player_name": "Bob" }
→ 200 { "game_id", "player_id" }
  404 unknown game
  409 game already started
```

The player is bound to the current actor: only that actor can open a WebSocket as this player. One actor may own several players.

```
GET /games/{id}
→ 200 { "id", "status", "owner_id", "question_list_id", "players": [{ "id", "name", "lives", "active" }],
        "current_q_idx", "question_open", "total_questions", "remaining_questions", "end_reason" }
  404 unknown game (or evicted)
```

`current_q_idx` is the index of the last started question (`-1` before the first). `question_open` is true while that question accepts answers. `remaining_questions` counts the questions not started yet. `end_reason` is empty until the game is finished, then holds the `game_over` reason.

```
POST /games/{id}/start        (host only)
→ 200 { "status": "question started" }, broadcasts question_started
  403 not the host
  409 code=question_open: the current question must be closed first
  409 code=no_more_questions: the last question has already been played
  409 code=game_finished: the game ended by elimination
```

```
POST /games/{id}/close        (host only)
→ 200 { "life_lost": [...], "eliminated": [...], "remaining_questions": 1,
        "game_over": false, "reason": "", "winner": "", "survivors": null }
  403 not the host
  409 code=game_not_running: game not started yet, or already finished
  409 code=no_active_question: no question is open (already closed by hand or by the timeout)
```

Close broadcasts `question_closed`, then `life_lost` (privately), `player_eliminated` and `game_over` as needed. `reason`, `winner` and `survivors` are only meaningful when `game_over` is true. `remaining_questions` can be above 0 on game over when the game ended by elimination. `life_lost` and `eliminated` are `null` when empty.

## WebSocket

```
ws://localhost:8080/ws?gameId=<game_id>&playerId=<player_id>
```

The connection is authenticated like any other request (session cookie, or debug headers / query parameters in dev mode), and the actor must be the one that created `playerId`.

| Status | Reason                                   |
|--------|------------------------------------------|
| `400`  | missing `gameId` or `playerId`           |
| `403`  | player not in the game, or owned by another actor |
| `404`  | unknown game                             |

A player has at most one connection. Reconnecting (for example after a page reload) closes the previous connection and events go to the new one.

The server pings every 54s and closes the connection if no pong arrives within 60s. Messages are limited to 4 KB.

### Server → client events

Every event has the shape `{ "type": "...", "payload": { ... } }`.

| `type`              | Sent to      | Payload                                                           |
|---------------------|--------------|-------------------------------------------------------------------|
| `game_joined`       | that player  | `game_id`, `player_id`, `status`, `question_list_id`, `total_questions` |
| `question_started`  | everyone     | `question_id`, `index`, `total`, `is_last`, `text`, `options`, `theme` (`{ id, name, scope }` or `null`), `answer_timeout_seconds` (only when the game has a timeout) |
| `answer_submitted`  | everyone     | `player_id`, `question_id` (correctness is not revealed)          |
| `question_closed`   | everyone     | `question_id`, `correct_option_id`, `remaining_questions`         |
| `life_lost`         | that player  | `player_id`, `lives_left`                                         |
| `player_eliminated` | everyone     | `player_id`                                                       |
| `game_over`         | everyone     | `reason`, `winner_id` (empty when there is no winner), `survivors` |

`game_joined` is written directly to the local hub, the other events go through Redis.

### Client → server messages

```json
{ "type": "submit_answer", "data": { "question_id": "...", "option_id": "b" } }
```

Only the first answer of a player to the open question counts. Rejected answers (no open question, wrong question, already answered, eliminated player) are logged and ignored, no error is sent back.

## Test UI

`tools/test-ui` is a Vite + Vue 3 tool to drive the API by hand: pick a debug actor, manage question lists, questions and themes, create a game, simulate up to 6 players and inspect every HTTP call and WebSocket event. It is started by `make up`. See [tools/test-ui/README.md](tools/test-ui/README.md).

## Test scenarios

These scenarios use the test UI in dev mode.

### Scenario 1: public quiz

1. In the actor bar, set the actor to **admin** with any ID (for example `admin-1`).
2. In the **Lists** tab, create a **public** list and add at least 3 questions.
3. Switch the actor to **user** (for example `user-1`).
4. Select the public list, click **use in game →**, then create a game in the **Game** tab. `user-1` is now the host.
5. Add 2 to 4 player cards, **join** each of them, then **connect ws**.
6. Click **▶ start question**: every card shows the question.
7. Answer from some of the cards.
8. Click **■ close question**: players who answered wrong or did not answer lose a life. The owner player has no card, so it loses a life on every question.
9. Repeat until `game_over`.

Keep the same actor from step 4 to the end: start and close are host only, and each card can only connect with the actor it joined with.

### Scenario 2: private quiz

1. Set the actor to **user** with ID `user-alice`.
2. Create a **private** list and add questions to it.
3. Check that it appears under the private lists.
4. Switch the actor ID to `user-bob`: the list is no longer visible.
5. Switch back to `user-alice`, select the list, click **use in game →**, create a game and play as in scenario 1.

### Scenario 3: access checks (all return 403)

- Create a **public** list as a `user`.
- Create a **private** list as an `admin`.
- Add a question to a public list as a `user`.
- Create a game from another user's private list.
- Start or close a question with an actor other than the host.
- Open a WebSocket for a player with an actor other than the one that joined.

## Tests and load tests

```bash
make test     # go test ./... -race -count=1
```

Unit tests cover the game engine, the manager sweeper and the dev auth middleware. Handlers and the Postgres layer have no automated tests yet.

Load tests use k6 against a running stack (`make up`). `BASE_URL` and `WS_URL` default to `localhost:$(HTTP_PORT)`.

```bash
make load-test-game   # 10 VUs for 2 minutes, each playing a full one-player game
make load-test-room   # NUM_PLAYERS players (default 50) in a single game
make load-test        # both
```

`load-test-room` also accepts `ANSWER_MIN_MS`, `ANSWER_MAX_MS` and `CLOSE_DELAY_MS`, for example `make load-test-room NUM_PLAYERS=200`.

## Useful commands

```bash
make up           # start api, postgres, redis and the test UI
make down         # stop all services
make logs         # tail API logs
make ui-logs      # tail test UI logs
make test         # run all tests with the race detector
make fmt          # gofmt (and goimports if installed)
make lint         # golangci-lint, falls back to go vet
make build        # build the binary into bin/api
make shell        # open a shell in the API container
make migrate-up   # restart the API, which re-applies migrations
```

## Known limitations

- **Game state does not survive a restart.** Games live in memory only; Postgres keeps a record of games and players but is never read back.
- **Single instance per game.** Events go through Redis pub/sub, but a game's state lives in the memory of the instance that created it. Running several API instances requires routing every request of a game (HTTP and WebSocket) to the same instance.
- **Development defaults.** The session cookie is not marked `Secure`, the WebSocket upgrader accepts any origin and `SESSION_SECRET` has a public default. All three must be changed before a public deployment.
- **Answers are readable.** `GET /question-lists/{id}/questions` returns `correct_option_id`, so any actor who can read a public list can see its answers.
- **Catalog editing.** Questions can be created and updated but not reordered or deleted; question lists can only be created.
