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
migrations/      — plain SQL migrations (run via golang-migrate)
```

**Key design decisions:**
- `chi` for HTTP routing — idiomatic, zero dependencies beyond stdlib
- `gorilla/websocket` — mature, explicit read/write pumps with ping/pong
- Game engine is fully in-memory with a clean `Broadcaster` interface so it can be swapped for a Redis pub/sub implementation without touching business logic
- One `Hub` per game — no global broadcast bus, straightforward fan-out
- Postgres and Redis are initialized at startup but the game state lives in memory for now; both stores are wired and ready for use
- Events are broadcast *after* releasing the game lock to avoid contention

## Prerequisites

- Docker & Docker Compose v2
- Go 1.22+ (for local runs without Docker)

## Quick start

```bash
cp .env.example .env   # adjust if needed
make up                # builds and starts api + postgres + redis
make migrate-up        # run SQL migrations
```

The API is available at `http://localhost:8080`.

## Environment variables

| Variable             | Default                                              | Description                         |
|----------------------|------------------------------------------------------|-------------------------------------|
| `HTTP_ADDR`          | `:8080`                                              | Address the server listens on       |
| `DATABASE_URL`       | `postgres://quizz:quizz@localhost:5432/quizz?...`   | PostgreSQL DSN                      |
| `REDIS_ADDR`         | `localhost:6379`                                     | Redis address                       |
| `REDIS_PASSWORD`     | *(empty)*                                            | Redis password                      |
| `LOG_LEVEL`          | `info`                                               | `debug` / `info` / `warn` / `error` |
| `GAME_INITIAL_LIVES` | `3`                                                  | Lives per player                    |
| `SHUTDOWN_TIMEOUT`   | `10s`                                                | Graceful shutdown window            |

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

### Join game

```
POST /games/{id}/join
{ "player_name": "Bob" }
→ { "game_id": "...", "player_id": "..." }
```

### Get game state

```
GET /games/{id}
→ { "id": "...", "status": "waiting", "players": [...], ... }
```

### Add question (owner only — auth not yet implemented)

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

## WebSocket

Connect at `ws://localhost:8080/ws?gameId=...&playerId=...`

**Server → client events:**

| `type`               | Description                                          |
|----------------------|------------------------------------------------------|
| `game_joined`        | Sent on successful connection                        |
| `question_started`   | A new question has been broadcast                    |
| `answer_submitted`   | A player submitted an answer (correctness hidden)    |
| `question_closed`    | Question ended; correct option revealed              |
| `life_lost`          | Sent privately to the player who lost a life         |
| `player_eliminated`  | A player reached 0 lives                             |
| `game_over`          | Last player standing determined                      |

**Client → server messages:**

```json
{ "type": "submit_answer", "data": { "question_id": "...", "option_id": "b" } }
```

## Useful commands

```bash
make up           # start all services
make down         # stop all services
make logs         # tail API logs
make test         # run all tests with race detector
make fmt          # format code
make lint         # run golangci-lint (falls back to go vet)
make migrate-up   # apply pending migrations
make migrate-down # rollback last migration
make shell        # sh into the API container
make build        # build binary locally
```
