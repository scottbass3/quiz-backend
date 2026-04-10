package ws

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

// Hub manages all WebSocket clients for a single game.
// It implements game.Broadcaster.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Client // playerID → client
	logger  *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients: make(map[string]*Client),
		logger:  logger,
	}
}

func (h *Hub) Register(playerID string, c *Client) {
	h.mu.Lock()
	h.clients[playerID] = c
	h.mu.Unlock()
}

func (h *Hub) Unregister(playerID string) {
	h.mu.Lock()
	delete(h.clients, playerID)
	h.mu.Unlock()
}

// Broadcast sends an event to every connected client.
func (h *Hub) Broadcast(event domain.Event) {
	msg, err := json.Marshal(event)
	if err != nil {
		h.logger.Error("hub: marshal event", "error", err)
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.clients {
		c.send(msg)
	}
}

// BroadcastTo sends an event to a single player.
func (h *Hub) BroadcastTo(playerID string, event domain.Event) {
	msg, err := json.Marshal(event)
	if err != nil {
		h.logger.Error("hub: marshal event", "error", err)
		return
	}

	h.mu.RLock()
	c, ok := h.clients[playerID]
	h.mu.RUnlock()

	if ok {
		c.send(msg)
	}
}

// ConnectedCount returns the number of active WebSocket connections.
func (h *Hub) ConnectedCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
