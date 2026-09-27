package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
	"github.com/scottbass3/quizz-backend/internal/handler"
	appredis "github.com/scottbass3/quizz-backend/internal/redis"
)

// instance is one API instance: its own session store (hubs, subscriptions),
// game manager and HTTP server, all sharing one Redis with the others.
type instance struct {
	manager *game.Manager
	srv     *httptest.Server
}

func newInstances(t *testing.T, n int) []*instance {
	t.Helper()
	mr := miniredis.RunT(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	instances := make([]*instance, n)
	for i := range instances {
		rdb := appredis.New(mr.Addr(), "")
		sessions := newGameSessionStore(rdb, time.Hour, logger)
		manager := game.NewManager(appredis.NewGameStateStore(rdb.Unwrap(), time.Hour, time.Minute), sessions.Broadcaster, logger)
		h := handler.NewGameHandler(manager, sessions, nil, nil, nil, game.EngineConfig{InitialLives: 3}, logger)
		srv := httptest.NewServer(http.HandlerFunc(h.WebSocket))
		t.Cleanup(func() { srv.Close(); sessions.Stop() })
		instances[i] = &instance{manager: manager, srv: srv}
	}
	return instances
}

// newGame creates game "g1" with one question and player "p1", owned by the
// "anonymous" actor (no auth middleware in these tests).
func newGame(t *testing.T, m *game.Manager) *game.Engine {
	t.Helper()
	q := &domain.Question{ID: "q1", Text: "?", Options: []domain.Option{{ID: "a"}, {ID: "b"}}, CorrectOptionID: "a"}
	eng, err := m.Create(context.Background(), "g1", "p1", "", []*domain.Question{q, q}, game.EngineConfig{InitialLives: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.AddPlayer(context.Background(), "p1", "Alice", "anonymous"); err != nil {
		t.Fatal(err)
	}
	return eng
}

func dial(t *testing.T, inst *instance) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(inst.srv.URL, "http") + "/ws?gameId=g1&playerId=p1"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// nextEvent reads the next event, or returns "closed" / "timeout".
func nextEvent(conn *websocket.Conn, wait time.Duration) string {
	conn.SetReadDeadline(time.Now().Add(wait))
	var ev domain.Event
	if err := conn.ReadJSON(&ev); err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return "timeout"
		}
		return "closed"
	}
	return string(ev.Type)
}

// A player who reconnects through another instance keeps a single live
// connection: the old one, on the first instance, is closed and receives
// no more events.
func TestReconnectThroughAnotherInstance(t *testing.T) {
	inst := newInstances(t, 2)
	eng := newGame(t, inst[0].manager)

	oldConn := dial(t, inst[0])
	defer oldConn.Close()
	if ev := nextEvent(oldConn, 2*time.Second); ev != "game_joined" {
		t.Fatalf("old connection: expected game_joined, got %s", ev)
	}

	newConn := dial(t, inst[1])
	defer newConn.Close()
	if ev := nextEvent(newConn, 2*time.Second); ev != "game_joined" {
		t.Fatalf("new connection: expected game_joined, got %s", ev)
	}

	if ev := nextEvent(oldConn, 2*time.Second); ev != "closed" {
		t.Fatalf("the old connection on the other instance should be closed, got %s", ev)
	}

	if err := eng.StartNextQuestion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ev := nextEvent(newConn, 2*time.Second); ev != "question_started" {
		t.Fatalf("new connection should receive events, got %s", ev)
	}
}

// Connections opened at the same time on several instances settle on one:
// an event is delivered exactly once.
func TestConcurrentConnectionsSettleOnOne(t *testing.T) {
	inst := newInstances(t, 3)
	newGame(t, inst[0].manager)
	pub := inst[0].manager // any instance can publish

	for round := 0; round < 5; round++ {
		conns := make([]*websocket.Conn, len(inst)*2)
		var wg sync.WaitGroup
		for i := range conns {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				conns[i] = dial(t, inst[i%len(inst)])
			}(i)
		}
		wg.Wait()
		time.Sleep(300 * time.Millisecond) // let the claims reach every instance

		eng, err := pub.Get(context.Background(), "g1")
		if err != nil {
			t.Fatal(err)
		}
		// Broadcast through a real engine transition: player_joined.
		if err := eng.AddPlayer(context.Background(), "extra-"+string(rune('a'+round)), "Extra", "someone"); err != nil {
			t.Fatal(err)
		}

		received := 0
		for _, c := range conns {
			for {
				ev := nextEvent(c, 500*time.Millisecond)
				if ev == "player_joined" {
					received++
				}
				if ev != "game_joined" {
					break
				}
			}
			c.Close()
		}
		if received != 1 {
			t.Fatalf("round %d: event delivered to %d connections, want exactly 1", round, received)
		}
	}
}
