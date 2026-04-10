package game

import (
	"errors"
	"sync"
)

var ErrGameNotFound = errors.New("game not found")

// Manager holds active game engines in memory.
// When Redis pub/sub or persistence is needed, this can be replaced or wrapped.
type Manager struct {
	mu     sync.RWMutex
	games  map[string]*Engine
	cfg    EngineConfig
}

func NewManager(cfg EngineConfig) *Manager {
	return &Manager{
		games: make(map[string]*Engine),
		cfg:   cfg,
	}
}

func (m *Manager) Create(gameID, ownerID string, hub Broadcaster) *Engine {
	eng := NewEngine(gameID, ownerID, m.cfg, hub)

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
