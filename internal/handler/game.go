package handler

import (
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
		OwnerName      string `json:"owner_name"`
		QuestionListID string `json:"question_list_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OwnerName == "" {
		writeError(w, http.StatusBadRequest, "owner_name is required")
		return
	}
	if req.QuestionListID == "" {
		writeError(w, http.StatusBadRequest, "question_list_id is required")
		return
	}

	// Load and validate the question list.
	list, err := h.questionListStore.GetQuestionList(r.Context(), req.QuestionListID)
	if err != nil {
		writeError(w, http.StatusNotFound, "question list not found")
		return
	}
	if list.Visibility == "private" && list.OwnerID != a.ID {
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
	}

	gameID := uuid.NewString()
	ownerID := uuid.NewString()

	// GetOrCreate wires the Redis pub/sub broadcaster (for the engine) to the WS hub (for clients).
	broadcaster, _ := h.sessions.GetOrCreate(gameID)
	eng := h.manager.Create(gameID, ownerID, req.QuestionListID, questions, broadcaster)

	if err := eng.AddPlayer(ownerID, req.OwnerName); err != nil {
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
				Lives:     h.cfg.InitialLives,
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
	if err := eng.AddPlayer(playerID, req.PlayerName); err != nil {
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
			Lives:     h.cfg.InitialLives,
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

	snap := eng.Snapshot()

	type playerView struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Lives  int    `json:"lives"`
		Active bool   `json:"active"`
	}
	players := make([]playerView, 0, len(snap.Players))
	for _, p := range snap.Players {
		players = append(players, playerView{
			ID:     p.ID,
			Name:   p.Name,
			Lives:  p.Lives,
			Active: p.Active,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":               snap.ID,
		"status":           snap.Status,
		"owner_id":         snap.OwnerID,
		"question_list_id": snap.QuestionListID,
		"players":          players,
		"current_q_idx":    snap.CurrentQIdx,
		"total_questions":  len(snap.Questions),
	})
}

// POST /games/{id}/start — advance to the next question
func (h *GameHandler) StartNextQuestion(w http.ResponseWriter, r *http.Request) {
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

	if err := eng.StartNextQuestion(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
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

// POST /games/{id}/close — close the active question
func (h *GameHandler) CloseQuestion(w http.ResponseWriter, r *http.Request) {
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

	result, err := eng.CloseQuestion()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	// Best-effort: persist player life changes.
	if h.playerStore != nil {
		for _, delta := range result.LifeDeltas {
			if err := h.playerStore.UpdatePlayerLives(r.Context(), delta.PlayerID, delta.LivesLeft, delta.Active); err != nil {
				h.logger.Error("update player lives in postgres", "error", err, "player_id", delta.PlayerID)
			}
		}
	}

	// Best-effort: persist game status if the game is now finished.
	if result.GameOver && h.gameStore != nil {
		if err := h.gameStore.UpdateGameStatus(r.Context(), gameID, string(domain.GameStatusFinished)); err != nil {
			h.logger.Error("update game status (finished) in postgres", "error", err, "game_id", gameID)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"life_lost":  result.LifeLost,
		"eliminated": result.Eliminated,
		"game_over":  result.GameOver,
		"winner":     result.Winner,
	})
}

// GET /ws?gameId=...&playerId=...
func (h *GameHandler) WebSocket(w http.ResponseWriter, r *http.Request) {
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

	snap := eng.Snapshot()
	if _, ok := snap.Players[playerID]; !ok {
		writeError(w, http.StatusForbidden, "player not in game")
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
	defer hub.Unregister(playerID)

	// Notify the player they have joined.
	// BroadcastTo goes through Redis → subscriber → hub → WS client.
	snap2 := eng.Snapshot()
	hub.BroadcastTo(playerID, domain.Event{
		Type: domain.EventGameJoined,
		Payload: map[string]any{
			"game_id":          gameID,
			"player_id":        playerID,
			"status":           snap2.Status,
			"question_list_id": snap2.QuestionListID,
			"total_questions":  len(snap2.Questions),
		},
	})

	ctx := r.Context()

	// WritePump and ReadPump run in separate goroutines.
	go client.WritePump(ctx)
	client.ReadPump(ctx, h.makeMessageHandler(gameID))
}

func (h *GameHandler) makeMessageHandler(gameID string) appws.MessageHandler {
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
				return
			}
			if err := eng.SubmitAnswer(playerID, data.QuestionID, data.OptionID); err != nil {
				h.logger.Info("ws: submit answer rejected", "player_id", playerID, "error", err)
			}
		default:
			h.logger.Warn("ws: unknown message type", "type", msg.Type, "player_id", playerID)
		}
	}
}
