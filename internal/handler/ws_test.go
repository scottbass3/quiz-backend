package handler

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
	appws "github.com/scottbass3/quizz-backend/internal/ws"
)

// hubSessions serves one real hub for every game, as both the engine's
// broadcaster and the WebSocket hub (no Redis in between).
type hubSessions struct{ hub *appws.Hub }

func (s hubSessions) Broadcaster(string) game.Broadcaster { return s.hub }
func (s hubSessions) Acquire(string) (*appws.Hub, error)  { return s.hub, nil }
func (s hubSessions) Release(string)                      {}

// dialPlayer opens a WebSocket as playerID and consumes the game_joined handshake.
func dialPlayer(t *testing.T, srv *httptest.Server, playerID string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?gameId=g1&playerId=" + playerID
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", playerID, err)
	}
	var ev domain.Event
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&ev); err != nil || ev.Type != domain.EventGameJoined {
		t.Fatalf("expected game_joined, got %+v (err %v)", ev, err)
	}
	return conn
}

// When a player reconnects, the old connection is closed and must not
// unregister the new one when it goes away.
func TestWebSocketReconnectKeepsNewConnection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := appws.NewHub(logger)
	manager := game.NewManager()
	eng := manager.Create("g1", "owner", "", nil, game.EngineConfig{InitialLives: 3}, hub)
	// No auth middleware in this test: extractActor falls back to "anonymous".
	eng.AddPlayer("p1", "Alice", "anonymous")

	h := NewGameHandler(manager, hubSessions{hub}, nil, nil, nil, game.EngineConfig{InitialLives: 3}, logger)
	srv := httptest.NewServer(http.HandlerFunc(h.WebSocket))
	defer srv.Close()

	oldConn := dialPlayer(t, srv, "p1")
	defer oldConn.Close()
	newConn := dialPlayer(t, srv, "p1")
	defer newConn.Close()

	// The server closes the replaced connection.
	oldConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := oldConn.ReadMessage(); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("old connection was not closed by the server")
			}
			break
		}
	}
	// Give the old handler time to return and run its deferred cleanup.
	time.Sleep(200 * time.Millisecond)

	if n := hub.ConnectedCount(); n != 1 {
		t.Fatalf("expected the new connection to stay registered, got %d clients", n)
	}
	hub.Broadcast(domain.Event{Type: domain.EventQuestionStarted})
	var ev domain.Event
	newConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := newConn.ReadJSON(&ev); err != nil || ev.Type != domain.EventQuestionStarted {
		t.Fatalf("new connection should receive events, got %+v (err %v)", ev, err)
	}
}

func TestWebSocketOriginCheck(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := appws.NewHub(logger)
	manager := game.NewManager()
	eng := manager.Create("g1", "owner", "", nil, game.EngineConfig{InitialLives: 3}, hub)
	eng.AddPlayer("p1", "Alice", "anonymous")
	h := NewGameHandler(manager, hubSessions{hub}, nil, nil, nil, game.EngineConfig{InitialLives: 3}, logger)
	h.RestrictOrigins([]string{"https://ui.example.com"})
	srv := httptest.NewServer(http.HandlerFunc(h.WebSocket))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?gameId=g1&playerId=p1"

	cases := map[string]bool{"https://ui.example.com": true, "https://evil.example.com": false, "": true}
	for origin, ok := range cases {
		header := http.Header{}
		if origin != "" {
			header.Set("Origin", origin)
		}
		conn, resp, err := websocket.DefaultDialer.Dial(url, header)
		if ok && err != nil {
			t.Errorf("origin %q: expected success, got %v", origin, err)
		}
		if !ok && (err == nil || resp == nil || resp.StatusCode != http.StatusForbidden) {
			t.Errorf("origin %q: expected 403, got err=%v", origin, err)
		}
		if conn != nil {
			conn.Close()
		}
	}
}
