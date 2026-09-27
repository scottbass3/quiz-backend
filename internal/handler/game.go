package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
	"github.com/scottbass3/quizz-backend/internal/store"
	appws "github.com/scottbass3/quizz-backend/internal/ws"
)

// newUpgrader builds the WebSocket upgrader. With no allowed origins every
// origin is accepted (reverse proxies often rewrite Host, so a same-origin
// check would reject legitimate clients). Otherwise browsers may only connect
// from the listed origins; clients that send no Origin (non-browsers) are
// always accepted.
func newUpgrader(allowedOrigins []string) websocket.Upgrader {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			return len(allowed) == 0 || origin == "" || allowed[origin]
		},
	}
}

// gameSessionRegistry gives WebSocket clients the local hub of their game.
// Implemented by app.gameSessionStore.
type gameSessionRegistry interface {
	// Acquire returns the local hub of a game for a new WebSocket client;
	// each successful call must be paired with Release.
	Acquire(gameID string) (*appws.Hub, error)
	Release(gameID string)
}

type GameHandler struct {
	manager           *game.Manager
	sessions          gameSessionRegistry
	gameStore         store.GameStore
	playerStore       store.PlayerStore
	questionListStore store.QuestionListStore
	cfg               game.EngineConfig
	logger            *slog.Logger
	upgrader          websocket.Upgrader
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
	h := &GameHandler{
		manager:           manager,
		sessions:          sessions,
		gameStore:         gameStore,
		playerStore:       playerStore,
		questionListStore: questionListStore,
		cfg:               cfg,
		logger:            logger,
		upgrader:          newUpgrader(nil),
	}
	// Every close is persisted, whichever instance performs it (host request
	// or answer deadline).
	manager.OnQuestionClosed(h.persistClose)
	return h
}

// RestrictOrigins limits WebSocket handshakes from browsers to the given
// origins (see newUpgrader). Call it before serving requests.
func (h *GameHandler) RestrictOrigins(origins []string) {
	h.upgrader = newUpgrader(origins)
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
		}
		if qr.Theme != nil {
			questions[i].Theme = &domain.QuestionTheme{ID: qr.Theme.ID, Name: qr.Theme.Name, Scope: string(qr.Theme.Scope)}
		}
	}

	gameID := uuid.NewString()
	ownerID := uuid.NewString()

	eng, err := h.manager.Create(r.Context(), gameID, ownerID, req.QuestionListID, questions, engineCfg)
	if err != nil {
		h.logger.Error("create game", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create game")
		return
	}
	if err := eng.AddPlayer(r.Context(), ownerID, req.OwnerName, a.ID); err != nil {
		h.logger.Error("add owner player", "error", err, "game_id", gameID)
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

	eng, ok := h.getEngine(w, r, gameID)
	if !ok {
		return
	}

	playerID := uuid.NewString()
	if err := eng.AddPlayer(r.Context(), playerID, req.PlayerName, a.ID); err != nil {
		switch {
		case errors.Is(err, game.ErrGameAlreadyStarted):
			writeError(w, http.StatusConflict, "game already started")
		default:
			h.writeEngineError(w, "join game", err)
		}
		return
	}

	if h.playerStore != nil {
		s, err := eng.State(r.Context())
		if err != nil {
			h.logger.Error("load game after join", "error", err, "game_id", gameID)
		}
		lives := h.cfg.InitialLives
		if s != nil {
			lives = s.Config.InitialLives
		}
		if err := h.playerStore.CreatePlayer(r.Context(), store.PlayerRecord{
			ID:        playerID,
			GameID:    gameID,
			Name:      req.PlayerName,
			Lives:     lives,
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
	st, ok := h.loadState(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, gameView(st, extractActor(r).ID))
}

// getEngine returns the engine of the game, or writes 404/500.
func (h *GameHandler) getEngine(w http.ResponseWriter, r *http.Request, gameID string) (*game.Engine, bool) {
	eng, err := h.manager.Get(r.Context(), gameID)
	if err != nil {
		h.writeEngineError(w, "get game", err)
		return nil, false
	}
	return eng, true
}

// loadState loads the state of the game, or writes 404/500.
func (h *GameHandler) loadState(w http.ResponseWriter, r *http.Request, gameID string) (*game.State, bool) {
	eng, ok := h.getEngine(w, r, gameID)
	if !ok {
		return nil, false
	}
	st, err := eng.State(r.Context())
	if err != nil {
		h.writeEngineError(w, "load game", err)
		return nil, false
	}
	return st, true
}

// writeEngineError maps errors that are not about the game's state: unknown
// game (404), lock contention (503), anything else (500, logged).
func (h *GameHandler) writeEngineError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, game.ErrGameNotFound):
		writeError(w, http.StatusNotFound, "game not found")
	case errors.Is(err, game.ErrBusy):
		writeErrorCode(w, http.StatusServiceUnavailable, "game_busy", "game is busy, retry")
	default:
		h.logger.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// gameView is the full state of a game as seen by actorID: everything a
// client needs to rebuild its screen after a reload.
func gameView(st *game.State, actorID string) map[string]any {
	isHost, myPlayers := st.ActorView(actorID)
	return map[string]any{
		"id":                  st.ID,
		"status":              st.Status,
		"owner_id":            st.OwnerID,
		"question_list_id":    st.QuestionListID,
		"players":             st.Scoreboard(), // sorted by name
		"current_q_idx":       st.CurrentQIdx,
		"question_open":       st.QuestionOpen,
		"current_question":    st.CurrentQuestion(), // null unless a question is open
		"total_questions":     st.TotalQuestions,
		"remaining_questions": st.RemainingQuestions(),
		"end_reason":          st.EndReason,
		"me":                  map[string]any{"is_host": isHost, "player_ids": myPlayers},
	}
}

// GET /games — existing games where the current actor is the host or owns
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
	states, err := h.manager.GamesOf(r.Context(), actorID)
	if err != nil {
		h.writeEngineError(w, "list games", err)
		return
	}
	games := []myGame{}
	for _, st := range states {
		isHost, playerIDs := st.ActorView(actorID)
		if !isHost && len(playerIDs) == 0 {
			continue
		}
		games = append(games, myGame{
			ID:             st.ID,
			Status:         st.Status,
			QuestionListID: st.QuestionListID,
			TotalQuestions: st.TotalQuestions,
			CreatedAt:      st.CreatedAt.UTC(),
			IsHost:         isHost,
			PlayerIDs:      playerIDs,
		})
	}
	writeJSON(w, http.StatusOK, games)
}

// POST /games/{id}/start — advance to the next question (host only)
func (h *GameHandler) StartNextQuestion(w http.ResponseWriter, r *http.Request) {
	a := extractActor(r)
	gameID := chi.URLParam(r, "id")

	eng, ok := h.getEngine(w, r, gameID)
	if !ok {
		return
	}
	st, err := eng.State(r.Context())
	if err != nil {
		h.writeEngineError(w, "load game", err)
		return
	}
	if st.HostActorID() != a.ID {
		writeError(w, http.StatusForbidden, "only the game host can start questions")
		return
	}

	if err := eng.StartNextQuestion(r.Context()); err != nil {
		h.writeGameError(w, err)
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

	eng, ok := h.getEngine(w, r, gameID)
	if !ok {
		return
	}
	st, err := eng.State(r.Context())
	if err != nil {
		h.writeEngineError(w, "load game", err)
		return
	}
	if st.HostActorID() != a.ID {
		writeError(w, http.StatusForbidden, "only the game host can close questions")
		return
	}

	result, err := eng.CloseQuestion(r.Context())
	if err != nil {
		h.writeGameError(w, err)
		return
	}

	// Persistence of the result happens in persistClose, registered on the
	// manager so that deadline closes are persisted too.
	writeJSON(w, http.StatusOK, map[string]any{
		"life_lost":           result.LifeLost,
		"eliminated":          result.Eliminated,
		"game_over":           result.GameOver,
		"winner":              result.Winner,
		"survivors":           result.Survivors,
		"reason":              result.Reason,
		"remaining_questions": result.RemainingQuestions,
		"players":             result.Players,
	})
}

// persistTimeout bounds best-effort writes made outside a request (e.g. when
// the answer timer closes a question).
const persistTimeout = 5 * time.Second

// persistClose saves the outcome of a question close (lives, game status).
// Registered with Manager.OnQuestionClosed, so it runs for manual closes and
// for answer deadlines alike. Best-effort: errors are logged.
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
// Other errors (unknown game, busy, storage) go through writeEngineError.
func (h *GameHandler) writeGameError(w http.ResponseWriter, err error) {
	code := ""
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
	default:
		h.writeEngineError(w, "game transition", err)
		return
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

	st, ok := h.loadState(w, r, gameID)
	if !ok {
		return
	}

	actorID, ok := st.PlayerActorID(playerID)
	if !ok {
		writeError(w, http.StatusForbidden, "player not in game")
		return
	}
	if actorID != a.ID {
		writeError(w, http.StatusForbidden, "player belongs to another actor")
		return
	}

	// The local hub fans out the game's events (received from Redis) to this
	// instance's clients.
	hub, err := h.sessions.Acquire(gameID)
	if err != nil {
		h.logger.Error("ws: acquire game session", "error", err, "game_id", gameID)
		writeError(w, http.StatusServiceUnavailable, "event stream unavailable")
		return
	}
	defer h.sessions.Release(gameID)

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("ws upgrade", "error", err)
		return
	}

	client := appws.NewClient(conn, playerID, gameID, h.logger)
	hub.Register(playerID, client)
	defer hub.Unregister(playerID, client)

	// Notify the player they have joined. This goes straight to the local hub
	// (not through Redis) so it arrives before the pumps start. It carries the
	// open question, if any, so a reconnecting player can answer it. The state
	// is reloaded: it may have changed while the subscription was set up.
	if fresh, err := h.manager.Get(r.Context(), gameID); err == nil {
		if s, err := fresh.State(r.Context()); err == nil {
			st = s
		}
	}
	me := st.Players[playerID]
	hub.BroadcastTo(playerID, domain.Event{
		Type: domain.EventGameJoined,
		Payload: map[string]any{
			"game_id":          gameID,
			"player_id":        playerID,
			"is_host":          playerID == st.OwnerID,
			"lives":            me.Lives,
			"active":           me.Active,
			"status":           st.Status,
			"question_list_id": st.QuestionListID,
			"total_questions":  st.TotalQuestions,
			"current_question": st.CurrentQuestion(), // null unless a question is open
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
		ctx, cancel := context.WithTimeout(context.Background(), messageTimeout)
		defer cancel()
		eng, err := h.manager.Get(ctx, gameID)
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
			if err := eng.SubmitAnswer(ctx, playerID, data.QuestionID, data.OptionID); err != nil {
				h.logger.Info("ws: submit answer rejected", "player_id", playerID, "error", err)
				rejectAnswer(hub, playerID, data, answerRejectCode(err), err.Error())
			}
		default:
			h.logger.Warn("ws: unknown message type", "type", msg.Type, "player_id", playerID)
		}
	}
}

// messageTimeout bounds the handling of one client message.
const messageTimeout = 5 * time.Second

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
