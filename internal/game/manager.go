package game

import (
	"errors"
	"sync"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

var ErrGameNotFound = errors.New("game not found")

// Manager holds active game engines in memory.
type Manager struct {
	mu    sync.RWMutex
	games map[string]*Engine
}

func NewManager() *Manager {
	return &Manager{
		games: make(map[string]*Engine),
	}
}

// Create registers a new engine for the given game.
// questions is the pre-loaded set from the question list.
// cfg overrides the manager's default EngineConfig for this game.
func (m *Manager) Create(gameID, ownerID, questionListID string, questions []*domain.Question, cfg EngineConfig, hub Broadcaster) *Engine {
	eng := NewEngine(gameID, ownerID, questionListID, questions, cfg, hub)

	m.mu.Lock()
	m.games[gameID] = eng
	m.mu.Unlock()

	return eng
}

func (m *Manager) Get(gameID string) (*Engine, error) {
	m.mu.RLock()
	eng, ok := m.games[gameID]
	m.mu.RUnlock()

	if !ok {
		return nil, ErrGameNotFound
	}
	return eng, nil
}

func (m *Manager) Delete(gameID string) {
	m.mu.Lock()
	delete(m.games, gameID)
	m.mu.Unlock()
}

func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.games)
}
