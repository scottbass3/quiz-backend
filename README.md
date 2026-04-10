# quizz-backend

Real-time multiplayer quiz backend in Go. Inspired by Master of the Grid by Elisee.

## Architecture overview

```
cmd/api          — entry point: loads config, wires app, handles OS signals
internal/
  config/        — env-driven configuration
  domain/        — pure data types: Game, Player, Question, QuestionList, Answer, Event
  game/          — game engine (thread-safe, in-memory) + manager
  store/         — repository interfaces (GameStore, PlayerStore, QuestionListStore)
  postgres/      — pgx/v5 implementations of store interfaces
  redis/         — redis client wrapper (ready for pub/sub expansion)
  ws/            — Hub (broadcast) + Client (read/write pump per connection)
  handler/       — chi HTTP handlers + WebSocket upgrade
  app/           — wires all layers, runs the HTTP server
migrations/      — SQL embedded in the binary, applied automatically at startup
tools/test-ui/   — Vite + Vue 3 dev tool for manual testing
```

**Key design decisions:**
- Question lists (catalog) are decoupled from game sessions (runtime)
- Games reference a question list; questions are loaded once at game creation and held in memory
- `chi` for HTTP routing — idiomatic, zero dependencies beyond stdlib
- `gorilla/websocket` — mature, explicit read/write pumps with ping/pong
- Game engine is fully in-memory with a clean `Broadcaster` interface
- One `Hub` per game — no global broadcast bus, straightforward fan-out
- Events are broadcast *after* releasing the game lock to avoid contention
- Migrations are embedded in the binary (`//go:embed`) and run automatically on startup — no external migration tool required
- Auth is **not implemented** — identity is simulated via dev-only HTTP headers (see below)

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

## Dev-only identity simulation

Authentication is not implemented. Instead, all endpoints read two **dev-only headers** to simulate an actor:

| Header                | Values         | Default       |
|-----------------------|----------------|---------------|
| `X-Debug-Actor-Type`  | `admin`, `user`| `user`        |
| `X-Debug-Actor-Id`    | any string     | `anonymous`   |

**These headers must not be trusted in production.** Replace them with real auth middleware before any public deployment.

The test UI injects these headers automatically on every HTTP request. You can change the actor type and ID in the "actor (dev)" bar at the top of the sidebar.

## HTTP API

### Health

```
GET /health
→ { "status": "ok", "uptime": "1m23s" }
```

### Question lists

```
POST /question-lists
Headers: X-Debug-Actor-Type: admin   (public) or user (private)
         X-Debug-Actor-Id: <id>
{ "name": "General culture", "description": "...", "visibility": "public" }
→ { "id": "...", "name": "...", "visibility": "public", ... }

Rules:
  - visibility=public  → actor must be admin
  - visibility=private → actor must be user (list is owned by the actor id)
```

```
GET /question-lists/public
→ [{ "id": "...", "name": "...", ... }]
```

```
GET /question-lists/private
Headers: X-Debug-Actor-Type: user
         X-Debug-Actor-Id: <id>
→ lists owned by that actor id
```

```
GET /question-lists/{id}
→ list metadata (private lists only accessible by their owner)
```

```
GET /question-lists/{id}/questions
→ ordered list of questions in the catalog
```

```
POST /question-lists/{id}/questions
Headers: X-Debug-Actor-Type: admin  (for public lists)
                              user   (for private lists, must be owner)
{
  "text": "Capital of France?",
  "options": [{"id":"a","text":"London"},{"id":"b","text":"Paris"},{"id":"c","text":"Berlin"}],
  "correct_option_id": "b"
}
→ { "question_id": "..." }
```

### Games

```
POST /games
{ "owner_name": "Alice", "question_list_id": "..." }
→ { "game_id": "...", "owner_id": "...", "question_list_id": "...", "total_questions": 3 }
```

The owner is automatically added as a player. Questions are loaded from the list at creation and held in memory for the lifetime of the game.

Private lists can only be used by their owner (matched via `X-Debug-Actor-Id`).

```
POST /games/{id}/join
{ "player_name": "Bob" }
→ { "game_id": "...", "player_id": "..." }
```

```
GET /games/{id}
→ { "id": "...", "status": "waiting", "question_list_id": "...", "players": [...], "current_q_idx": -1, "total_questions": 3 }
```

```
POST /games/{id}/start
→ broadcasts `question_started` to all connected WS clients
```

```
POST /games/{id}/close
→ broadcasts `question_closed`, `life_lost`, `player_eliminated`, `game_over`
→ { "life_lost": [...], "eliminated": [...], "game_over": false, "winner": "" }
```

### WebSocket

Connect at `ws://localhost:8080/ws?gameId=...&playerId=...`

**Server → client events:**

| `type`               | Description                                       |
|----------------------|---------------------------------------------------|
| `game_joined`        | Sent on successful connection (includes total_questions, question_list_id) |
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

## Test scenarios

### Scenario 1 — Public quiz (admin flow)

1. In the test UI, set actor to **admin** with any ID (e.g. `admin-1`)
2. In "Question Lists", create a **public** list (e.g. "General Culture")
3. Add 3+ questions to the list
4. Switch actor to **user** (e.g. `user-1`)
5. In "Game", select the public list from the list panel, then click **create game**
6. Add 2–4 player cards, join each to the game, connect WS
7. Click **▶ start question** → all players see the question
8. Players click their answer
9. Click **■ close question** → scores update, lives deducted for wrong answers
10. Repeat for remaining questions

### Scenario 2 — Private quiz (user flow)

1. Set actor to **user** with ID `user-alice`
2. Create a **private** list (e.g. "Alice's trivia")
3. Add questions to the list
4. Verify the list appears in "my private lists"
5. Switch actor ID to `user-bob` → the list should not appear in Bob's private lists
6. Switch back to `user-alice`, select the list, create a game
7. Play as in scenario 1

### Scenario 3 — Access denied checks

- Try creating a **public** list with actor type `user` → 403
- Try creating a **private** list with actor type `admin` → 403
- Try creating a game with a private list belonging to another user → 403
- Try adding a question to a public list with actor type `user` → 403

## Useful commands

```bash
make up           # start all services (api + postgres + redis + test-ui)
make down         # stop all services
make logs         # tail API logs
make ui-logs      # tail test-ui logs
make test         # run all tests with race detector
make fmt          # format code
make lint         # run golangci-lint (falls back to go vet)
make shell        # sh into the API container
make build        # build binary locally
```

Migrations run automatically at startup — restart the API container to apply new migrations.
