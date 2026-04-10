# quizz-backend

Real-time multiplayer quiz backend in Go. Inspired by Master of the Grid by Elisee.

## Architecture overview

```
cmd/api          — entry point: loads config, wires app, handles OS signals
internal/
  config/        — env-driven configuration (no external lib needed)
  domain/        — pure data types: Game, Player, Question, Answer, Event
  game/          — game engine (thread-safe, in-memory) + manager
  store/         — repository interfaces (GameStore, PlayerStore, QuestionStore)
  postgres/      — pgx/v5 implementations of store interfaces
  redis/         — redis client wrapper (ready for pub/sub expansion)
  ws/            — Hub (broadcast) + Client (read/write pump per connection)
  handler/       — chi HTTP handlers + WebSocket upgrade
  app/           — wires all layers, runs the HTTP server
migrations/      — SQL embedded in the binary, applied automatically at startup
tools/test-ui/   — Vite + Vue 3 dev tool for manual testing
```

**Key design decisions:**
- `chi` for HTTP routing — idiomatic, zero dependencies beyond stdlib
- `gorilla/websocket` — mature, explicit read/write pumps with ping/pong
- Game engine is fully in-memory with a clean `Broadcaster` interface so it can be swapped for a Redis pub/sub implementation without touching business logic
- One `Hub` per game — no global broadcast bus, straightforward fan-out
- Postgres and Redis are initialized at startup but the game state lives in memory for now; both stores are wired and ready for use
- Events are broadcast *after* releasing the game lock to avoid contention
- Migrations are embedded in the binary (`//go:embed`) and run automatically on startup — no external migration tool required

## Prerequisites

- Docker & Docker Compose v2
- Go 1.23+ (for local runs without Docker)

## Quick start

```bash
cp .env.example .env   # adjust if needed
make up                # builds and starts api + postgres + redis + test-ui
```

Migrations run automatically when the API starts.

- API: `http://localhost:8080`
- Test UI: `http://localhost:5173`

## Environment variables

| Variable             | Default                                            | Description                         |
|----------------------|----------------------------------------------------|-------------------------------------|
| `HTTP_PORT`          | `8080`                                             | Host port exposed by Docker         |
| `HTTP_ADDR`          | `:8080`                                            | Address the server listens on       |
| `DATABASE_URL`       | `postgres://quizz:quizz@localhost:5432/quizz?...` | PostgreSQL DSN                      |
| `REDIS_ADDR`         | `localhost:6379`                                   | Redis address                       |
| `REDIS_PASSWORD`     | *(empty)*                                          | Redis password                      |
| `LOG_LEVEL`          | `info`                                             | `debug` / `info` / `warn` / `error` |
| `GAME_INITIAL_LIVES` | `3`                                                | Lives per player                    |
| `SHUTDOWN_TIMEOUT`   | `10s`                                              | Graceful shutdown window            |
| `UI_PORT`            | `5173`                                             | Host port for the test UI           |

## HTTP API

### Health

```
GET /health
→ { "status": "ok", "uptime": "1m23s" }
```

### Create game

```
POST /games
{ "owner_name": "Alice" }
→ { "game_id": "...", "owner_id": "..." }
```

The owner is automatically added as a player.

### Join game

```
POST /games/{id}/join
{ "player_name": "Bob" }
→ { "game_id": "...", "player_id": "..." }
```

Only works while the game is in `waiting` status.

### Get game state

```
GET /games/{id}
→ { "id": "...", "status": "waiting", "players": [...], "current_q_idx": -1, "total_questions": 0 }
```

### Add question

```
POST /games/{id}/questions
{
  "text": "Capital of France?",
  "options": [{"id":"a","text":"London"},{"id":"b","text":"Paris"},{"id":"c","text":"Berlin"}],
  "correct_option_id": "b"
}
→ { "question_id": "..." }
```

### Start next question

```
POST /games/{id}/start
→ broadcasts `question_started` to all connected WS clients
```

### Close active question

```
POST /games/{id}/close
→ broadcasts `question_closed`, `life_lost`, `player_eliminated`, `game_over`
→ { "life_lost": [...], "eliminated": [...], "game_over": false, "winner": "" }
```

Players who did not answer or answered incorrectly lose one life. `life_lost` is sent privately to each affected player.

## WebSocket

Connect at `ws://localhost:8080/ws?gameId=...&playerId=...`

The player must have joined via `POST /games/{id}/join` before connecting.

**Server → client events:**

| `type`               | Description                                       |
|----------------------|---------------------------------------------------|
| `game_joined`        | Sent on successful connection                     |
| `question_started`   | New question broadcast; includes options          |
| `answer_submitted`   | A player submitted an answer (correctness hidden) |
| `question_closed`    | Question ended; correct option revealed           |
| `life_lost`          | Sent privately to the player who lost a life      |
| `player_eliminated`  | A player reached 0 lives                          |
| `game_over`          | Last player standing determined                   |

**Client → server messages:**

```json
{ "type": "submit_answer", "data": { "question_id": "...", "option_id": "b" } }
```

Only the first submission per player per question is recorded.

## Useful commands

```bash
make up           # start all services (api + postgres + redis + test-ui)
make down         # stop all services
make logs         # tail API logs
make ui-logs      # tail test-ui logs
make test         # run all tests with race detector
make fmt          # format code
make lint         # run golangci-lint (falls back to go vet)
make migrate-up   # restart API to reapply embedded migrations
make shell        # sh into the API container
make build        # build binary locally
```
