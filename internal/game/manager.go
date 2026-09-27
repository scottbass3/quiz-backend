package game

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

// Manager is the entry point to games: it creates them, hands out engines,
// and closes questions whose answer deadline has passed. It keeps no game
// state: all of it lives in the StateStore.
type Manager struct {
	store       StateStore
	broadcaster func(gameID string) Broadcaster
	logger      *slog.Logger

	mu      sync.RWMutex
	onClose func(gameID string, result *CloseQuestionResult)
}

// NewManager builds a manager over store. broadcaster returns the event
// publisher of a game.
func NewManager(store StateStore, broadcaster func(gameID string) Broadcaster, logger *slog.Logger) *Manager {
	return &Manager{store: store, broadcaster: broadcaster, logger: logger}
}

// OnQuestionClosed registers fn to be called after every question close,
// whether triggered by CloseQuestion or by an answer deadline, on whichever
// instance performed it. Used for persistence, which the engine knows
// nothing about.
func (m *Manager) OnQuestionClosed(fn func(gameID string, result *CloseQuestionResult)) {
	m.mu.Lock()
	m.onClose = fn
	m.mu.Unlock()
}

func (m *Manager) questionClosed(gameID string, result *CloseQuestionResult) {
	m.mu.RLock()
	fn := m.onClose
	m.mu.RUnlock()
	if fn != nil {
		fn(gameID, result)
	}
}

func (m *Manager) engine(gameID string) *Engine {
	return &Engine{m: m, id: gameID, hub: m.broadcaster(gameID)}
}

// Create stores a new game (status waiting, no players) with its copy of the
// questions. The caller then adds the host's player with AddPlayer.
func (m *Manager) Create(ctx context.Context, gameID, ownerID, questionListID string, questions []*domain.Question, cfg EngineConfig) (*Engine, error) {
	s := &State{
		ID:             gameID,
		Status:         domain.GameStatusWaiting,
		OwnerID:        ownerID,
		QuestionListID: questionListID,
		CreatedAt:      time.Now(),
		Config:         cfg,
		TotalQuestions: len(questions),
		CurrentQIdx:    -1,
		Players:        map[string]*domain.Player{},
	}
	if err := m.store.Create(ctx, s, questions); err != nil {
		return nil, err
	}
	return m.engine(gameID), nil
}

// Get returns the engine of an existing game, or ErrGameNotFound.
func (m *Manager) Get(ctx context.Context, gameID string) (*Engine, error) {
	ok, err := m.store.Exists(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrGameNotFound
	}
	return m.engine(gameID), nil
}

// Exists reports whether a game still exists.
func (m *Manager) Exists(ctx context.Context, gameID string) (bool, error) {
	return m.store.Exists(ctx, gameID)
}

// GamesOf returns the existing games where actorID is the host or owns a
// player, newest first.
func (m *Manager) GamesOf(ctx context.Context, actorID string) ([]*State, error) {
	ids, err := m.store.GamesOfActor(ctx, actorID)
	if err != nil {
		return nil, err
	}
	states := make([]*State, 0, len(ids))
	for _, id := range ids {
		s, err := m.store.Load(ctx, id)
		if errors.Is(err, ErrGameNotFound) {
			continue // expired since the index was read
		}
		if err != nil {
			return nil, err
		}
		states = append(states, s)
	}
	sort.SliceStable(states, func(i, j int) bool { return states[i].CreatedAt.After(states[j].CreatedAt) })
	return states, nil
}

// dueBatch bounds the number of deadlines handled per CloseDue call.
const dueBatch = 100

// CloseDue closes the questions whose answer deadline is at or before now.
// Deadlines are claimed atomically, so with several instances each one is
// handled once. Returns the number of questions closed.
func (m *Manager) CloseDue(ctx context.Context, now time.Time) int {
	due, err := m.store.ClaimDueCloses(ctx, now, dueBatch)
	if err != nil {
		m.logger.Error("claim answer deadlines", "error", err)
		return 0
	}
	closed := 0
	for _, d := range due {
		_, err := m.engine(d.GameID).closeQuestion(ctx, d.Index)
		switch {
		case err == nil:
			closed++
		case errors.Is(err, ErrNoActiveQuestion), errors.Is(err, ErrGameNotRunning), errors.Is(err, ErrGameNotFound):
			// Already closed by the host, finished, or expired: nothing to do.
		default:
			m.logger.Error("close question at deadline", "error", err, "game_id", d.GameID, "index", d.Index)
		}
	}
	return closed
}

// RunDeadlines calls CloseDue every interval until ctx is done.
func (m *Manager) RunDeadlines(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.CloseDue(ctx, now)
		}
	}
}
