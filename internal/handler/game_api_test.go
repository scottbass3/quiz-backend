package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/scottbass3/quizz-backend/internal/auth"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
	appws "github.com/scottbass3/quizz-backend/internal/ws"
)

// gameServer serves the game routes as wired in app.New, behind the dev-mode
// auth middleware, with one real hub shared by every game and a catalog that
// always holds one public list with two questions ("list-1").
func gameServer(t *testing.T) (*httptest.Server, *game.Manager) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := newMemStore()
	m.lists["list-1"] = memList("list-1")
	for i, id := range []string{"q1", "q2"} {
		m.questions[id] = memQuestion(id, "list-1", i)
	}
	manager := game.NewManager()
	hub := appws.NewHub(logger)
	h := NewGameHandler(manager, hubSessions{hub}, nil, nil, m, game.EngineConfig{InitialLives: 3}, logger)

	r := chi.NewRouter()
	r.Use(auth.Middleware([]byte("test"), false))
	r.Route("/games", func(r chi.Router) {
		r.Post("/", h.CreateGame)
		r.Get("/", h.ListMyGames)
		r.Get("/{id}", h.GetGame)
		r.Post("/{id}/join", h.JoinGame)
		r.Post("/{id}/start", h.StartNextQuestion)
		r.Post("/{id}/close", h.CloseQuestion)
	})
	r.Get("/ws", h.WebSocket)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, manager
}

type gameState struct {
	Status          string         `json:"status"`
	Players         []playerView   `json:"players"`
	CurrentQuestion map[string]any `json:"current_question"`
	Me              struct {
		IsHost    bool     `json:"is_host"`
		PlayerIDs []string `json:"player_ids"`
	} `json:"me"`
}

// createAndJoin creates a game hosted by alice and joined by bob.
func createAndJoin(t *testing.T, srv *httptest.Server) (gameID, ownerID, bobID string) {
	t.Helper()
	var created map[string]any
	if code := call(t, srv, alice, "POST", "/games", `{"owner_name":"Alice","question_list_id":"list-1"}`, &created); code != http.StatusCreated {
		t.Fatalf("create game: %d", code)
	}
	var joined map[string]string
	call(t, srv, bob, "POST", "/games/"+created["game_id"].(string)+"/join", `{"player_name":"Bob"}`, &joined)
	return created["game_id"].(string), created["owner_id"].(string), joined["player_id"]
}

func TestGetGame_MeAndCurrentQuestion(t *testing.T) {
	srv, _ := gameServer(t)
	gameID, ownerID, bobID := createAndJoin(t, srv)

	var st gameState
	call(t, srv, alice, "GET", "/games/"+gameID, "", &st)
	if !st.Me.IsHost || len(st.Me.PlayerIDs) != 1 || st.Me.PlayerIDs[0] != ownerID {
		t.Fatalf("alice: unexpected me %+v", st.Me)
	}
	if st.CurrentQuestion != nil {
		t.Fatalf("no current question before start, got %v", st.CurrentQuestion)
	}
	if len(st.Players) != 2 || st.Players[0].Name != "Alice" || st.Players[1].Name != "Bob" {
		t.Fatalf("players should be sorted by name, got %+v", st.Players)
	}

	call(t, srv, bob, "GET", "/games/"+gameID, "", &st)
	if st.Me.IsHost || len(st.Me.PlayerIDs) != 1 || st.Me.PlayerIDs[0] != bobID {
		t.Fatalf("bob: unexpected me %+v", st.Me)
	}

	call(t, srv, alice, "POST", "/games/"+gameID+"/start", "", nil)
	call(t, srv, bob, "GET", "/games/"+gameID, "", &st)
	if st.CurrentQuestion == nil || st.CurrentQuestion["question_id"] != "q1" {
		t.Fatalf("expected q1 as current question, got %v", st.CurrentQuestion)
	}
	if _, leaks := st.CurrentQuestion["correct_option_id"]; leaks {
		t.Fatal("current_question must not reveal the answer")
	}
}

func TestListMyGames(t *testing.T) {
	srv, _ := gameServer(t)
	gameID, _, bobID := createAndJoin(t, srv)

	type myGame struct {
		ID        string   `json:"id"`
		IsHost    bool     `json:"is_host"`
		PlayerIDs []string `json:"player_ids"`
	}
	var games []myGame
	call(t, srv, bob, "GET", "/games", "", &games)
	if len(games) != 1 || games[0].ID != gameID || games[0].IsHost || games[0].PlayerIDs[0] != bobID {
		t.Fatalf("bob: unexpected games %+v", games)
	}
	call(t, srv, alice, "GET", "/games", "", &games)
	if len(games) != 1 || !games[0].IsHost {
		t.Fatalf("alice: unexpected games %+v", games)
	}
	carol := actor{"user", "carol"}
	if code := call(t, srv, carol, "GET", "/games", "", &games); code != http.StatusOK || len(games) != 0 {
		t.Fatalf("carol: expected no games, got %d %+v", code, games)
	}
}

// A player who (re)connects while a question is open receives it in game_joined.
func TestGameJoinedCarriesOpenQuestion(t *testing.T) {
	srv, _ := gameServer(t)
	gameID, _, bobID := createAndJoin(t, srv)
	call(t, srv, alice, "POST", "/games/"+gameID+"/start", "", nil)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?gameId=" + gameID + "&playerId=" + bobID + "&debugActorId=bob"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var ev struct {
		Type    domain.EventType `json:"type"`
		Payload struct {
			Lives           int            `json:"lives"`
			IsHost          bool           `json:"is_host"`
			CurrentQuestion map[string]any `json:"current_question"`
		} `json:"payload"`
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(&ev); err != nil || ev.Type != domain.EventGameJoined {
		t.Fatalf("expected game_joined, got %+v (err %v)", ev, err)
	}
	if ev.Payload.Lives != 3 || ev.Payload.IsHost {
		t.Fatalf("unexpected player info %+v", ev.Payload)
	}
	if ev.Payload.CurrentQuestion == nil || ev.Payload.CurrentQuestion["question_id"] != "q1" {
		t.Fatalf("expected the open question in game_joined, got %v", ev.Payload.CurrentQuestion)
	}
	b, _ := json.Marshal(ev.Payload.CurrentQuestion)
	if strings.Contains(string(b), "correct_option_id") {
		t.Fatal("game_joined must not reveal the answer")
	}
}
