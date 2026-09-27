package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
	"github.com/scottbass3/quizz-backend/internal/store"
	appws "github.com/scottbass3/quizz-backend/internal/ws"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow all origins in development. Restrict in production.
		return true
	},
}

// gameSessionRegistry provides access to per-game broadcasters and WS hubs.
// Implemented by app.gameSessionStore.
type gameSessionRegistry interface {
	// GetOrCreate returns the broadcaster (used by the engine) and the hub (used for WS registration).
	// Both are created on the first call for a given gameID; subsequent calls return the same pair.
	GetOrCreate(gameID string) (game.Broadcaster, *appws.Hub)
	// GetHub returns the WS hub for an existing game.
	GetHub(gameID string) (*appws.Hub, bool)
}

type GameHandler struct {
	manager           *game.Manager
	sessions          gameSessionRegistry
	gameStore         store.GameStore
	playerStore       store.PlayerStore
	questionListStore store.QuestionListStore
	cfg               game.EngineConfig
	logger            *slog.Logger
}

func NewGameHandler(
	manager *game.Manager,
	sessions gameSessionRegistry,
	gameStore store.GameStore,
	playerStore store.PlayerStore,
	questionListStore store.QuestionListStore,
	cfg game.EngineConfig,
	logger *slog.Logger,
) *GameHandler {
	return &GameHandler{
		manager:           manager,
		sessions:          sessions,
		gameStore:         gameStore,
		playerStore:       playerStore,
		questionListStore: questionListStore,
		cfg:               cfg,
		logger:            logger,
	}
}

// POST /games
func (h *GameHandler) CreateGame(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)

	var req struct {
		OwnerName            string `json:"owner_name"`
		QuestionListID       string `json:"question_list_id"`
		InitialLives         *int   `json:"initial_lives"`          // optional; defaults to cfg
		AnswerTimeoutSeconds *int   `json:"answer_timeout_seconds"` // optional; 0 = no timeout
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OwnerName == "" {
		writeError(w, http.StatusBadRequest, "owner_name is required")
		return
	}
	if req.QuestionListID == "" {
		writeError(w, http.StatusBadRequest, "question_list_id is required")
		return
	}

	// Build per-game engine config, falling back to application defaults.
	engineCfg := h.cfg
	if req.InitialLives != nil {
		if *req.InitialLives < 1 {
			writeError(w, http.StatusBadRequest, "initial_lives must be at least 1")
			return
		}
		engineCfg.InitialLives = *req.InitialLives
	}
	if req.AnswerTimeoutSeconds != nil {
		if *req.AnswerTimeoutSeconds < 0 {
			writeError(w, http.StatusBadRequest, "answer_timeout_seconds must be non-negative")
			return
		}
		engineCfg.AnswerTimeoutSeconds = *req.AnswerTimeoutSeconds
	}

	// Load and validate the question list.
	list, err := h.questionListStore.GetQuestionList(r.Context(), req.QuestionListID)
	if err != nil {
		writeError(w, http.StatusNotFound, "question list not found")
		return
	}
	if !canReadList(a, list) {
		writeError(w, http.StatusForbidden, "cannot use another user's private list")
		return
	}

	// Load questions from the catalog.
	qrecs, err := h.questionListStore.ListQuestions(r.Context(), req.QuestionListID)
	if err != nil {
		h.logger.Error("load questions for game", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load questions")
		return
	}

	if len(qrecs) == 0 {
		writeErrorCode(w, http.StatusBadRequest, "empty_question_list", "question list has no questions")
		return
	}

	questions := make([]*domain.Question, len(qrecs))
	for i, qr := range qrecs {
		opts := make([]domain.Option, len(qr.Options))
		for j, o := range qr.Options {
			opts[j] = domain.Option{ID: o.ID, Text: o.Text}
		}
		questions[i] = &domain.Question{
			ID:              qr.ID,
			QuestionListID:  qr.QuestionListID,
			Text:            qr.Text,
			Options:         opts,
			CorrectOptionID: qr.CorrectOptionID,
			OrderIndex:      qr.OrderIndex,
			Answers:         make(map[string]*domain.Answer),
		}
		if qr.Theme != nil {
			questions[i].Theme = &domain.QuestionTheme{ID: qr.Theme.ID, Name: qr.Theme.Name, Scope: string(qr.Theme.Scope)}
		}
	}

	gameID := uuid.NewString()
	ownerID := uuid.NewString()

	// GetOrCreate wires the Redis pub/sub broadcaster (for the engine) to the WS hub (for clients).
	broadcaster, _ := h.sessions.GetOrCreate(gameID)
	eng := h.manager.Create(gameID, ownerID, req.QuestionListID, questions, engineCfg, broadcaster)
	eng.OnQuestionClosed(func(res *game.CloseQuestionResult) { h.persistClose(gameID, res) })

	if err := eng.AddPlayer(ownerID, req.OwnerName, a.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add owner")
		return
	}

	now := time.Now().UTC()
	if h.gameStore != nil {
		if err := h.gameStore.CreateGame(r.Context(), store.GameRecord{
			ID:             gameID,
			OwnerID:        ownerID,
			QuestionListID: req.QuestionListID,
			Status:         string(domain.GameStatusWaiting),
			CreatedAt:      now,
			UpdatedAt:      now,
		}); err != nil {
			h.logger.Error("create game in postgres", "error", err)
		}
		if h.playerStore != nil {
			if err := h.playerStore.CreatePlayer(r.Context(), store.PlayerRecord{
				ID:        ownerID,
				GameID:    gameID,
				Name:      req.OwnerName,
				Lives:     engineCfg.InitialLives,
				Active:    true,
				CreatedAt: now,
			}); err != nil {
				h.logger.Error("create owner player in postgres", "error", err)
			}
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"game_id":          gameID,
		"owner_id":         ownerID,
		"question_list_id": req.QuestionListID,
		"total_questions":  len(questions),
	})
}

// POST /games/{id}/join
func (h *GameHandler) JoinGame(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	gameID := chi.URLParam(r, "id")

	var req struct {
		PlayerName string `json:"player_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PlayerName == "" {
		writeError(w, http.StatusBadRequest, "player_name is required")
		return
	}

	eng, err := h.manager.Get(gameID)
	if err != nil {
		if errors.Is(err, game.ErrGameNotFound) {
			writeError(w, http.StatusNotFound, "game not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	playerID := uuid.NewString()
	if err := eng.AddPlayer(playerID, req.PlayerName, a.ID); err != nil {
		switch {
		case errors.Is(err, game.ErrGameAlreadyStarted):
			writeError(w, http.StatusConflict, "game already started")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if h.playerStore != nil {
		if err := h.playerStore.CreatePlayer(r.Context(), store.PlayerRecord{
			ID:        playerID,
			GameID:    gameID,
			Name:      req.PlayerName,
			Lives:     eng.Config().InitialLives,
			Active:    true,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			h.logger.Error("create player in postgres", "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"game_id":   gameID,
		"player_id": playerID,
	})
}

// GET /games/{id}
func (h *GameHandler) GetGame(w http.ResponseWriter, r *http.Request) {
	gameID := chi.URLParam(r, "id")

	eng, err := h.manager.Get(gameID)
	if err != nil {
		if errors.Is(err, game.ErrGameNotFound) {
			writeError(w, http.StatusNotFound, "game not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, gameView(eng, extractActor(r).ID))
}

// playerView is the public view of a player.
type playerView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Lives  int    `json:"lives"`
	Active bool   `json:"active"`
}

// playerViews lists the players sorted by name, then ID, for a stable order.
func playerViews(snap domain.Game) []playerView {
	players := make([]playerView, 0, len(snap.Players))
	for _, p := range snap.Players {
		players = append(players, playerView{ID: p.ID, Name: p.Name, Lives: p.Lives, Active: p.Active})
	}
	sort.Slice(players, func(i, j int) bool {
		if players[i].Name != players[j].Name {
			return players[i].Name < players[j].Name
		}
		return players[i].ID < players[j].ID
	})
	return players
}

// gameView is the full state of a game as seen by actorID: everything a
// client needs to rebuild its screen after a reload.
func gameView(eng *game.Engine, actorID string) map[string]any {
	snap := eng.Snapshot()
	isHost, myPlayers := eng.ActorView(actorID)
	return map[string]any{
		"id":                  snap.ID,
		"status":              snap.Status,
		"owner_id":            snap.OwnerID,
		"question_list_id":    snap.QuestionListID,
		"players":             playerViews(snap),
		"current_q_idx":       snap.CurrentQIdx,
		"question_open":       snap.QuestionOpen,
		"current_question":    eng.CurrentQuestion(), // null unless a question is open
		"total_questions":     len(snap.Questions),
		"remaining_questions": len(snap.Questions) - snap.CurrentQIdx - 1,
		"end_reason":          snap.EndReason,
		"me":                  map[string]any{"is_host": isHost, "player_ids": myPlayers},
	}
}

// GET /games — games in memory where the current actor is the host or owns
// a player, newest first. Lets a client find its games again after losing
// its local state.
func (h *GameHandler) ListMyGames(w http.ResponseWriter, r *http.Request) {
	actorID := extractActor(r).ID

	type myGame struct {
		ID             string            `json:"id"`
		Status         domain.GameStatus `json:"status"`
		QuestionListID string            `json:"question_list_id"`
		TotalQuestions int               `json:"total_questions"`
		CreatedAt      time.Time         `json:"created_at"`
		IsHost         bool              `json:"is_host"`
		PlayerIDs      []string          `json:"player_ids"`
	}
	games := []myGame{}
	for _, eng := range h.manager.All() {
		isHost, playerIDs := eng.ActorView(actorID)
		if !isHost && len(playerIDs) == 0 {
			continue
		}
		snap := eng.Snapshot()
		games = append(games, myGame{
			ID:             snap.ID,
			Status:         snap.Status,
			QuestionListID: snap.QuestionListID,
			TotalQuestions: len(snap.Questions),
			CreatedAt:      snap.CreatedAt.UTC(),
			IsHost:         isHost,
			PlayerIDs:      playerIDs,
		})
	}
	sort.Slice(games, func(i, j int) bool { return games[i].CreatedAt.After(games[j].CreatedAt) })
	writeJSON(w, http.StatusOK, games)
}

// POST /games/{id}/start — advance to the next question (host only)
func (h *GameHandler) StartNextQuestion(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	gameID := chi.URLParam(r, "id")

	eng, err := h.manager.Get(gameID)
	if err != nil {
		if errors.Is(err, game.ErrGameNotFound) {
			writeError(w, http.StatusNotFound, "game not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if eng.HostActorID() != a.ID {
		writeError(w, http.StatusForbidden, "only the game host can start questions")
		return
	}

	if err := eng.StartNextQuestion(); err != nil {
		writeGameError(w, err)
		return
	}

	// Best-effort: persist game status transition to "running".
	if h.gameStore != nil {
		if err := h.gameStore.UpdateGameStatus(r.Context(), gameID, string(domain.GameStatusRunning)); err != nil {
			h.logger.Error("update game status in postgres", "error", err, "game_id", gameID)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "question started"})
}

// POST /games/{id}/close — close the active question (host only)
func (h *GameHandler) CloseQuestion(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	gameID := chi.URLParam(r, "id")

	eng, err := h.manager.Get(gameID)
	if err != nil {
		if errors.Is(err, game.ErrGameNotFound) {
			writeError(w, http.StatusNotFound, "game not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if eng.HostActorID() != a.ID {
		writeError(w, http.StatusForbidden, "only the game host can close questions")
		return
	}

	result, err := eng.CloseQuestion()
	if err != nil {
		writeGameError(w, err)
		return
	}

	// Persistence of the result happens in persistClose, registered on the
	// engine at creation so that timeout closes are persisted too.
	writeJSON(w, http.StatusOK, map[string]any{
		"life_lost":           result.LifeLost,
		"eliminated":          result.Eliminated,
		"game_over":           result.GameOver,
		"winner":              result.Winner,
		"survivors":           result.Survivors,
		"reason":              result.Reason,
		"remaining_questions": result.RemainingQuestions,
	})
}

// persistTimeout bounds best-effort writes made outside a request (e.g. when
// the answer timer closes a question).
const persistTimeout = 5 * time.Second

// persistClose saves the outcome of a question close (lives, game status).
// Registered with Engine.OnQuestionClosed, so it runs for manual closes and
// for answer timeouts alike. Best-effort: errors are logged.
func (h *GameHandler) persistClose(gameID string, result *game.CloseQuestionResult) {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()

	if h.playerStore != nil {
		for _, delta := range result.LifeDeltas {
			if err := h.playerStore.UpdatePlayerLives(ctx, delta.PlayerID, delta.LivesLeft, delta.Active); err != nil {
				h.logger.Error("update player lives in postgres", "error", err, "player_id", delta.PlayerID)
			}
		}
	}
	if result.GameOver && h.gameStore != nil {
		if err := h.gameStore.UpdateGameStatus(ctx, gameID, string(domain.GameStatusFinished)); err != nil {
			h.logger.Error("update game status (finished) in postgres", "error", err, "game_id", gameID)
		}
	}
}

// writeGameError maps an engine state error to 409 with a stable code the
// frontend can switch on (e.g. "no_more_questions" once the list is exhausted).
func writeGameError(w http.ResponseWriter, err error) {
	code := "conflict"
	switch {
	case errors.Is(err, game.ErrNoMoreQuestions):
		code = "no_more_questions"
	case errors.Is(err, game.ErrGameFinished):
		code = "game_finished"
	case errors.Is(err, game.ErrGameNotRunning):
		code = "game_not_running"
	case errors.Is(err, game.ErrNoActiveQuestion):
		code = "no_active_question"
	case errors.Is(err, game.ErrQuestionOpen):
		code = "question_open"
	}
	writeErrorCode(w, http.StatusConflict, code, err.Error())
}

// GET /ws?gameId=...&playerId=...
// The authenticated actor must be the one who created playerId.
func (h *GameHandler) WebSocket(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	gameID := r.URL.Query().Get("gameId")
	playerID := r.URL.Query().Get("playerId")

	if gameID == "" || playerID == "" {
		writeError(w, http.StatusBadRequest, "gameId and playerId are required")
		return
	}

	eng, err := h.manager.Get(gameID)
	if err != nil {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}

	actorID, ok := eng.PlayerActorID(playerID)
	if !ok {
		writeError(w, http.StatusForbidden, "player not in game")
		return
	}
	if actorID != a.ID {
		writeError(w, http.StatusForbidden, "player belongs to another actor")
		return
	}

	// The hub handles local WS connection management; the broadcaster routes events via Redis.
	hub, ok := h.sessions.GetHub(gameID)
	if !ok {
		writeError(w, http.StatusNotFound, "game session not found")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("ws upgrade", "error", err)
		return
	}

	client := appws.NewClient(conn, playerID, gameID, h.logger)
	hub.Register(playerID, client)
	defer hub.Unregister(playerID, client)

	// Notify the player they have joined. This goes straight to the local hub
	// (not through Redis) so it arrives before the pumps start. It carries the
	// open question, if any, so a reconnecting player can answer it.
	snap := eng.Snapshot()
	me := snap.Players[playerID]
	hub.BroadcastTo(playerID, domain.Event{
		Type: domain.EventGameJoined,
		Payload: map[string]any{
			"game_id":          gameID,
			"player_id":        playerID,
			"is_host":          playerID == snap.OwnerID,
			"lives":            me.Lives,
			"active":           me.Active,
			"status":           snap.Status,
			"question_list_id": snap.QuestionListID,
			"total_questions":  len(snap.Questions),
			"current_question": eng.CurrentQuestion(), // null unless a question is open
		},
	})

	ctx := r.Context()

	// WritePump and ReadPump run in separate goroutines.
	go client.WritePump(ctx)
	client.ReadPump(ctx, h.makeMessageHandler(gameID, hub))
}

// makeMessageHandler routes client messages to the engine. A rejected answer
// is reported to that player only, through the local hub (the connection is
// on this instance), as answer_rejected with a stable code.
func (h *GameHandler) makeMessageHandler(gameID string, hub *appws.Hub) appws.MessageHandler {
	return func(playerID string, msg appws.IncomingMessage) {
		eng, err := h.manager.Get(gameID)
		if err != nil {
			return
		}

		switch msg.Type {
		case "submit_answer":
			var data appws.SubmitAnswerData
			if err := json.Unmarshal(msg.Data, &data); err != nil {
				h.logger.Warn("ws: invalid submit_answer payload", "player_id", playerID, "error", err)
				rejectAnswer(hub, playerID, data, "invalid_message", "invalid submit_answer payload")
				return
			}
			if err := eng.SubmitAnswer(playerID, data.QuestionID, data.OptionID); err != nil {
				h.logger.Info("ws: submit answer rejected", "player_id", playerID, "error", err)
				rejectAnswer(hub, playerID, data, answerRejectCode(err), err.Error())
			}
		default:
			h.logger.Warn("ws: unknown message type", "type", msg.Type, "player_id", playerID)
		}
	}
}

func rejectAnswer(hub *appws.Hub, playerID string, data appws.SubmitAnswerData, code, msg string) {
	hub.BroadcastTo(playerID, domain.Event{
		Type: domain.EventAnswerRejected,
		Payload: map[string]any{
			"question_id": data.QuestionID,
			"option_id":   data.OptionID,
			"code":        code,
			"error":       msg,
		},
	})
}

// answerRejectCode maps SubmitAnswer errors to the codes sent in answer_rejected.
func answerRejectCode(err error) string {
	switch {
	case errors.Is(err, game.ErrNoActiveQuestion):
		return "no_active_question"
	case errors.Is(err, game.ErrWrongQuestion):
		return "wrong_question"
	case errors.Is(err, game.ErrAlreadyAnswered):
		return "already_answered"
	case errors.Is(err, game.ErrInvalidOption):
		return "invalid_option"
	case errors.Is(err, game.ErrPlayerEliminated):
		return "player_eliminated"
	case errors.Is(err, game.ErrGameNotRunning):
		return "game_not_running"
	default:
		return "rejected"
	}
}
