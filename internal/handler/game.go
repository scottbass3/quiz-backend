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

// hubRegistry maps gameID → Hub so the WS handler can look up the right hub.
type hubRegistry interface {
	GetOrCreate(gameID string) *appws.Hub
	Get(gameID string) (*appws.Hub, bool)
}

type GameHandler struct {
	manager  *game.Manager
	hubs     hubRegistry
	gameStore store.GameStore
	playerStore store.PlayerStore
	logger   *slog.Logger
}

func NewGameHandler(
	manager *game.Manager,
	hubs hubRegistry,
	gameStore store.GameStore,
	playerStore store.PlayerStore,
	logger *slog.Logger,
) *GameHandler {
	return &GameHandler{
		manager:     manager,
		hubs:        hubs,
		gameStore:   gameStore,
		playerStore: playerStore,
		logger:      logger,
	}
}

// POST /games
func (h *GameHandler) CreateGame(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OwnerName string `json:"owner_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OwnerName == "" {
		writeError(w, http.StatusBadRequest, "owner_name is required")
		return
	}

	gameID := uuid.NewString()
	ownerID := uuid.NewString()

	hub := h.hubs.GetOrCreate(gameID)
	eng := h.manager.Create(gameID, ownerID, hub)

	if err := eng.AddPlayer(ownerID, req.OwnerName); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add owner")
		return
	}

	now := time.Now().UTC()
	if h.gameStore != nil {
		if err := h.gameStore.CreateGame(r.Context(), store.GameRecord{
			ID:        gameID,
			OwnerID:   ownerID,
			Status:    string(domain.GameStatusWaiting),
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			h.logger.Error("create game in postgres", "error", err)
			// non-fatal: game lives in memory
		}
		if h.playerStore != nil {
			if err := h.playerStore.CreatePlayer(r.Context(), store.PlayerRecord{
				ID:        ownerID,
				GameID:    gameID,
				Name:      req.OwnerName,
				Lives:     3,
				Active:    true,
				CreatedAt: now,
			}); err != nil {
				h.logger.Error("create player in postgres", "error", err)
			}
		}
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"game_id":  gameID,
		"owner_id": ownerID,
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
			Lives:     3,
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
		"id":            snap.ID,
		"status":        snap.Status,
		"owner_id":      snap.OwnerID,
		"players":       players,
		"current_q_idx": snap.CurrentQIdx,
		"total_questions": len(snap.Questions),
	})
}

// POST /games/{id}/questions  — add a question to a game (owner only, for now unguarded)
func (h *GameHandler) AddQuestion(w http.ResponseWriter, r *http.Request) {
	gameID := chi.URLParam(r, "id")

	var req struct {
		Text            string `json:"text"`
		Options         []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"options"`
		CorrectOptionID string `json:"correct_option_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Text == "" || len(req.Options) < 2 || req.CorrectOptionID == "" {
		writeError(w, http.StatusBadRequest, "text, at least 2 options and correct_option_id are required")
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

	opts := make([]domain.Option, len(req.Options))
	for i, o := range req.Options {
		id := o.ID
		if id == "" {
			id = uuid.NewString()
		}
		opts[i] = domain.Option{ID: id, Text: o.Text}
	}

	q := &domain.Question{
		ID:              uuid.NewString(),
		Text:            req.Text,
		Options:         opts,
		CorrectOptionID: req.CorrectOptionID,
		Answers:         make(map[string]*domain.Answer),
	}

	if err := eng.AddQuestion(q); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"question_id": q.ID})
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

	hub, ok := h.hubs.Get(gameID)
	if !ok {
		writeError(w, http.StatusNotFound, "game hub not found")
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
	hub.BroadcastTo(playerID, domain.Event{
		Type: domain.EventGameJoined,
		Payload: map[string]any{
			"game_id":   gameID,
			"player_id": playerID,
			"status":    snap.Status,
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
