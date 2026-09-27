# quizz-backend

Real-time multiplayer quiz backend in Go, inspired by Master of the Grid by Elisee.

Players join a game, answer multiple-choice questions over WebSocket and lose a life for every wrong or missing answer. The last player standing wins.

Building a UI on top of this backend? Read the [frontend integration guide](docs/frontend-integration.md).

## Contents

- [Game rules](#game-rules)
- [Architecture](#architecture)
- [Getting started](#getting-started) (including [production](#production))
- [Configuration](#configuration)
- [Authentication](#authentication)
- [HTTP API](#http-api)
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
  game/          game rules (engine), game state view and the StateStore interface
  store/         repository interfaces (GameStore, PlayerStore, QuestionListStore)
  postgres/      pgx/v5 implementation of the store interfaces
  redis/         Redis client, game state store (Lua scripts, TTLs) and event pub/sub
  ws/            per-game Hub (fan-out) and Client (read/write pumps per connection)
  auth/          auth middleware, OIDC provider, session and state JWTs
  handler/       chi HTTP handlers and the WebSocket upgrade
  app/           wires all layers, runs the HTTP server, answer deadlines and local WS sessions
migrations/      SQL embedded in the binary, applied automatically at startup
k6/              load test scripts
tools/test-ui/   Vite + Vue 3 dev tool for manual testing (not production code)
```

### Request flow

```
HTTP / WS request
  → chi router, auth middleware        (internal/app, internal/auth)
  → handler                            (internal/handler)
  → game.Engine                        (internal/game)      game rules
  → redis.GameStateStore               (internal/redis)     game state, shared by every instance
  → redis publisher / subscriber       (internal/redis)     events on game:<id>:events
  → ws.Hub                             (internal/ws)        forwards events to this instance's clients
  → postgres.DB                        (internal/postgres)  best-effort persistence
```

### Key design decisions

"Redis" below means the Redis protocol: the Compose files run [Dragonfly](https://www.dragonflydb.io/), a Redis-compatible store, and any Redis-compatible server works.

- **Catalog vs runtime.** Question lists, their questions and themes live in Postgres. A game copies the questions of its list (with their theme) once, at creation, and never reads Postgres again for them.
- **Game state in Redis, rules in Go.** The `game` package applies the rules; all game state (players, lives, current question, answers) lives in Redis behind a `StateStore` interface. Any API instance can serve any request of any game, and a game survives an API restart.
- **Concurrency without an in-memory lock.** Join, start and close take a short per-game Redis lock. Answers skip it: a Lua script records the first answer only if the question is still open, and closing a question flips it closed and reads its answers in one atomic step, so an accepted answer is always counted.
- **Shared answer deadlines.** Timed questions register their deadline in a Redis sorted set that every instance polls; claiming a deadline is atomic, so each question is closed once, even if the instance that started it is gone.
- **Transport-agnostic engine.** The engine only knows a `Broadcaster` interface. Events are published on Redis (`game:<id>:events`) from any instance; each instance subscribes to a game while it has WebSocket clients of that game, and forwards the events to them.
- **One connection per player across instances.** Each WebSocket connection gets a generation from a per-player Redis counter and announces it on the game's channel; every instance closes its older connection of that player. The newest connection always wins, whatever the order of the messages, so reconnects through a load balancer never leave a duplicate.
- **Best-effort persistence.** Postgres keeps a history of games and players (lives, status). Writes are logged on failure but never abort a request, and are never read back for gameplay.
- **Expiry.** Game data expires in Redis: `GAME_FINISHED_TTL` after the end of a game, `GAME_IDLE_TTL` after the last activity of an unfinished one. Instances then close the WebSocket connections of expired games.
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
make up                # builds and starts api, postgres, dragonfly and the test UI
```

- API: `http://localhost:8080`
- Test UI: `http://localhost:5173`

The API runs with [air](https://github.com/air-verse/air) and rebuilds on every `.go` change. Migrations run automatically when the API starts.

### Production

`Dockerfile.dev` is the development image: it ships the Go toolchain, compiles and hot-reloads the mounted sources with air at container start, and runs as root. It is only used by `docker-compose.yml`.

`Dockerfile` is the production image: a static binary on distroless, running as a non-root user (about 23 MB), with migrations embedded. Its health check runs `/api healthcheck`, which probes `/health`.

`docker-compose.prod.yml` runs the production stack:

| Service     | Role                                                                                  |
|-------------|---------------------------------------------------------------------------------------|
| `api`       | the production image, `API_REPLICAS` replicas (default 2), not published              |
| `lb`        | nginx round-robin over every replica (`deploy/nginx.conf`), the only published port (`HTTP_PORT`, default 80) |
| `postgres`  | catalog and history (password required)                                              |
| `dragonfly` | state of running games (password required, snapshot every minute)                    |

```bash
cp .env.prod.example .env.prod   # fill in the secrets and the OIDC settings
make prod-up
```

- `SESSION_SECRET`, `POSTGRES_PASSWORD` and `REDIS_PASSWORD` are required: Compose refuses to start without them. `.env.prod` is git-ignored.
- OIDC is on and cookies are `Secure` by default. Terminate TLS in front of `lb` (reverse proxy or cloud load balancer); `OIDC_REDIRECT_URL` must be the public URL of `/auth/callback`.
- Scale with `docker compose -f docker-compose.prod.yml --env-file .env.prod up -d --scale api=4`: nginx re-resolves the replicas every 10 s, no reload needed.
- The test UI is a development tool and is not part of this stack.

### Several instances

`make up-multi` also starts `api2`, a second API instance sharing Postgres and Redis (port `HTTP_PORT_2`, default 8081), and `lb`, an nginx round-robin load balancer in front of both (port `LB_PORT`, default 8090). Any request of any game, WebSocket reconnects included, can go to either instance: `make load-test-multi` plays one game across them and `make lb-check` reconnects a player repeatedly through the load balancer. In production, every instance must use the same `SESSION_SECRET` (sessions are signed cookies checked by whichever instance receives the request), and the load balancer must allow WebSocket upgrades with a read timeout above the 54 s server ping (see `tools/lb/nginx.conf`).

### Without Docker (API only)

The API reads its configuration from the process environment only, it does not load `.env` by itself.

```bash
docker compose up -d postgres dragonfly
set -a && . ./.env && set +a
go run ./cmd/api
```

## Configuration

### Read by the API

| Variable             | Default                                             | Description                                                |
|----------------------|-----------------------------------------------------|------------------------------------------------------------|
| `HTTP_ADDR`          | `:8080`                                             | Address the server listens on                              |
| `DATABASE_URL`       | `postgres://quizz:quizz@localhost:5432/quizz?...`   | PostgreSQL DSN                                             |
| `REDIS_ADDR`         | `localhost:6379`                                    | Address of the Redis-compatible store (Dragonfly)          |
| `REDIS_PASSWORD`     | *(empty)*                                           | Password of that store                                     |
| `LOG_LEVEL`          | `info`                                              | `debug`, `info`, `warn` or `error`                         |
| `GAME_INITIAL_LIVES` | `3`                                                 | Lives per player                                           |
| `GAME_FINISHED_TTL`  | `10m`                                               | How long a finished game stays in the store                |
| `GAME_IDLE_TTL`      | `2h`                                                | Expire unfinished games with no activity for this long     |
| `SHUTDOWN_TIMEOUT`   | `10s`                                               | Graceful shutdown window                                   |
| `SESSION_SECRET`     | `dev-secret-change-in-production`                   | HMAC key for session and OAuth2 state JWTs (same on every instance) |
| `SESSION_COOKIE_SECURE` | `false`                                          | Mark the session and state cookies `Secure` (HTTPS only)   |
| `OIDC_ENABLED`       | `false`                                             | `true` to use OIDC instead of the debug headers            |
| `OIDC_ISSUER_URL`    | *(empty)*                                           | OIDC issuer (used for discovery)                           |
| `OIDC_CLIENT_ID`     | *(empty)*                                           | OAuth2 client ID                                           |
| `OIDC_CLIENT_SECRET` | *(empty)*                                           | OAuth2 client secret                                       |
| `OIDC_REDIRECT_URL`  | `http://localhost:8080/auth/callback`               | Callback URL registered at the provider (points to the API) |
| `OIDC_FRONTEND_URL`  | `http://localhost:5173`                             | Where the browser is sent after a successful login         |
| `OIDC_ROLE_CLAIM`    | `role`                                              | ID token claim holding the role (string or string array)   |
| `OIDC_ADMIN_ROLE`    | `admin`                                             | Role value that maps to the `admin` actor type             |
| `CORS_ALLOWED_ORIGINS` | *(empty)*                                         | Comma-separated browser origins allowed to call the API cross-origin with credentials; when set, WebSocket origins are limited to them |

Durations use Go syntax (`90s`, `10m`, `2h`). Invalid values fall back to the default.

### Used by Docker Compose only

| Variable            | Default | Description                     |
|---------------------|---------|---------------------------------|
| `HTTP_PORT`         | `8080`  | Host port mapped to the API     |
| `HTTP_PORT_2`       | `8081`  | Host port of `api2` (`make up-multi`) |
| `LB_PORT`           | `8090`  | Host port of the load balancer (`make up-multi`) |
| `UI_PORT`           | `5173`  | Host port mapped to the test UI |
| `POSTGRES_USER`     | `quizz` | Postgres user                   |
| `POSTGRES_PASSWORD` | `quizz` | Postgres password               |
| `POSTGRES_DB`       | `quizz` | Postgres database               |
| `POSTGRES_PORT`     | `5432`  | Host port mapped to Postgres    |
| `DRAGONFLY_PORT`    | `6379`  | Host port mapped to Dragonfly   |

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

This is a summary. Request and response bodies, every WebSocket payload, error codes and TypeScript types are in the [frontend integration guide](docs/frontend-integration.md), which is the reference for clients.

All bodies are JSON. Errors are `{ "error": "...", "code": "..." }`, where `code` is only set on errors clients are expected to handle.

| Method & path                                         | Who                     | Purpose                                              |
|-------------------------------------------------------|-------------------------|------------------------------------------------------|
| `GET /health`                                         | anyone                  | liveness, no auth                                    |
| `GET /auth/login`, `GET /auth/callback`               | anyone                  | OIDC login flow (`404` in dev mode)                  |
| `POST /auth/logout`                                   | anyone                  | clear the session cookie                             |
| `GET /auth/me`                                        | authenticated           | current actor and `oidc_enabled`                     |
| `POST /question-lists`                                | admin: public, user: private | create a list                                   |
| `GET /question-lists/public`, `/private`              | everyone / users        | list lists                                           |
| `GET /question-lists/{id}`                            | list readers            | list metadata                                        |
| `PUT`, `DELETE /question-lists/{id}`                  | list editors            | rename, delete (with its questions and themes)       |
| `GET /question-lists/{id}/questions[?theme_id=]`      | list readers            | questions; `correct_option_id` only for editors      |
| `POST /question-lists/{id}/questions`                 | list editors            | add a question (optional `theme_id`)                 |
| `PUT`, `DELETE /question-lists/{id}/questions/{qid}`  | list editors            | replace, delete a question                           |
| `PUT /question-lists/{id}/questions/order`            | list editors            | reorder all questions                                |
| `GET`, `POST /themes`; `GET`, `PUT`, `DELETE /themes/{tid}` | everyone reads, admins write | global themes                            |
| `GET`, `POST /question-lists/{id}/themes`; `GET`, `PUT`, `DELETE .../themes/{tid}` | readers / editors | custom themes of a list |
| `POST /games`                                         | authenticated           | create a game (optional `initial_lives`, `answer_timeout_seconds`); caller becomes host |
| `GET /games`                                          | authenticated           | games where the caller is host or owns a player      |
| `GET /games/{id}`                                     | authenticated           | full state, `current_question`, and `me`             |
| `POST /games/{id}/join`                               | authenticated           | add a player owned by the caller                     |
| `POST /games/{id}/start`, `/close`                    | host                    | start the next question, close the open one          |
| `GET /ws?gameId=&playerId=`                           | the player's actor      | WebSocket                                            |

List readers: everyone for a public list, the owner for a private one. List editors: any admin for a public list, the owner for a private one. Global themes are written by admins only.

## WebSocket

Connect to `/ws?gameId=<game_id>&playerId=<player_id>` as the actor that created the player (in dev mode, pass `debugActorType` / `debugActorId` as query parameters, since browsers cannot set headers on a WebSocket). One connection per player: reconnecting closes the previous connection. The server pings every 54 s.

| Server event        | Sent to     | Meaning                                                     |
|---------------------|-------------|-------------------------------------------------------------|
| `game_joined`       | that player | handshake; includes lives and the open question, if any     |
| `player_joined`     | everyone    | a player joined                                             |
| `question_started`  | everyone    | question, options, theme, `is_last`, optional `closes_at`   |
| `answer_submitted`  | everyone    | a player's answer was accepted (correctness hidden)         |
| `answer_rejected`   | that player | the answer was refused, with a code                         |
| `question_closed`   | everyone    | correct option, remaining questions, scoreboard             |
| `life_lost`         | that player | lives left                                                  |
| `player_eliminated` | everyone    | a player reached 0 lives                                    |
| `game_over`         | everyone    | `reason`, `winner_id`, `survivors`                          |

The only client message is `{ "type": "submit_answer", "data": { "question_id": "...", "option_id": "..." } }`. `game_joined` is written directly to the local hub; the other events go through Redis.

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

Game and handler tests run against [miniredis](https://github.com/alicebob/miniredis), an in-process Redis, including scenarios with several instances sharing one game. There is no Postgres test database: SQL is checked against the Docker stack.

Load tests use k6 against a running stack (`make up`). `BASE_URL` and `WS_URL` default to `localhost:$(HTTP_PORT)`.

```bash
make load-test-game   # 10 VUs for 2 minutes, each playing a full one-player game
make load-test-room   # NUM_PLAYERS players (default 50) in a single game
make load-test-multi  # one game spread over api and api2 (needs make up-multi)
make lb-check         # reconnect a player through the load balancer (needs make up-multi)
make load-test        # game and room
```

`load-test-room` also accepts `ANSWER_MIN_MS`, `ANSWER_MAX_MS` and `CLOSE_DELAY_MS`, for example `make load-test-room NUM_PLAYERS=200`.

## Useful commands

```bash
make up           # start api, postgres, dragonfly and the test UI
make up-multi     # same, plus a second API instance (api2, port 8081) and a load balancer (port 8090)
make down         # stop all services
make logs         # tail API logs
make ui-logs      # tail test UI logs
make test         # run all tests with the race detector
make fmt          # gofmt (and goimports if installed)
make lint         # golangci-lint, falls back to go vet
make build        # build the binary into bin/api
make prod-up      # production stack (docker-compose.prod.yml, settings in .env.prod)
make prod-down    # stop it
make prod-logs    # tail its API logs
make shell        # open a shell in the API container
make migrate-up   # restart the API, which re-applies migrations
```

## Known limitations

- **The game store is required to play.** Dragonfly holds every running game. It snapshots to disk every minute and reloads the snapshot at startup: a crash of Dragonfly can lose up to a minute of game activity.
- **Development defaults.** `docker-compose.yml` is for development: `SESSION_SECRET` has a public default, cookies are not `Secure`, identity comes from debug headers and, without `CORS_ALLOWED_ORIGINS`, the WebSocket upgrader accepts any origin. Use the production stack below for anything public.
- **Same-site only in OIDC mode.** The session cookie is `SameSite=Lax`: a UI on another site than the API cannot use it, even with CORS.
