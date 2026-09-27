package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
	"github.com/scottbass3/quizz-backend/internal/store"
	appws "github.com/scottbass3/quizz-backend/internal/ws"
)

func TestWriteGameError(t *testing.T) {
	h := &GameHandler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{game.ErrNoMoreQuestions, http.StatusConflict, "no_more_questions"},
		{game.ErrGameFinished, http.StatusConflict, "game_finished"},
		{game.ErrGameNotRunning, http.StatusConflict, "game_not_running"},
		{game.ErrNoActiveQuestion, http.StatusConflict, "no_active_question"},
		{game.ErrQuestionOpen, http.StatusConflict, "question_open"},
		{game.ErrBusy, http.StatusServiceUnavailable, "game_busy"},
		{game.ErrGameNotFound, http.StatusNotFound, ""},
		{errors.New("redis down"), http.StatusInternalServerError, ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.writeGameError(rec, c.err)

		var body map[string]string
		json.NewDecoder(rec.Body).Decode(&body)
		if rec.Code != c.status || body["code"] != c.code {
			t.Errorf("%v: got %d %v, want %d code=%q", c.err, rec.Code, body, c.status, c.code)
		}
	}
}

// emptyListStore serves a single public list that has no questions.
type emptyListStore struct{ store.QuestionListStore }

func (emptyListStore) GetQuestionList(context.Context, string) (*store.QuestionListRecord, error) {
	return &store.QuestionListRecord{ID: "list-1", Visibility: "public"}, nil
}

func (emptyListStore) ListQuestions(context.Context, string) ([]store.QuestionRecord, error) {
	return nil, nil
}

func TestCreateGame_RejectsEmptyList(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := game.EngineConfig{InitialLives: 3}
	h := NewGameHandler(testManager(t, nopBroadcaster{}), nil, nil, nil, emptyListStore{}, cfg, logger)

	req := httptest.NewRequest(http.MethodPost, "/games", strings.NewReader(`{"owner_name":"Alice","question_list_id":"list-1"}`))
	rec := httptest.NewRecorder()
	h.CreateGame(rec, req)

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if rec.Code != http.StatusBadRequest || body["code"] != "empty_question_list" {
		t.Fatalf("expected 400 empty_question_list, got %d %v", rec.Code, body)
	}
}

// oneQuestionStore serves a single public list with one question.
type oneQuestionStore struct{ store.QuestionListStore }

func (oneQuestionStore) GetQuestionList(context.Context, string) (*store.QuestionListRecord, error) {
	return &store.QuestionListRecord{ID: "list-1", Visibility: "public"}, nil
}

func (oneQuestionStore) ListQuestions(context.Context, string) ([]store.QuestionRecord, error) {
	return []store.QuestionRecord{{
		ID:              "q1",
		Text:            "2+2?",
		Options:         []store.OptionRecord{{ID: "a", Text: "3"}, {ID: "b", Text: "4"}},
		CorrectOptionID: "b",
	}}, nil
}

type nopBroadcaster struct{}

func (nopBroadcaster) Broadcast(domain.Event)           {}
func (nopBroadcaster) BroadcastTo(string, domain.Event) {}

type fakeSessions struct{}

func (fakeSessions) Broadcaster(string) game.Broadcaster { return nopBroadcaster{} }
func (fakeSessions) Acquire(string) (*appws.Hub, error)  { return nil, errors.New("no hub") }
func (fakeSessions) Release(string)                      {}

// recordingStore records the persistence calls made for a game.
type recordingStore struct {
	store.GameStore
	store.PlayerStore

	mu       sync.Mutex
	lives    map[string]int
	statuses []string
}

func (s *recordingStore) CreateGame(context.Context, store.GameRecord) error     { return nil }
func (s *recordingStore) CreatePlayer(context.Context, store.PlayerRecord) error { return nil }

func (s *recordingStore) UpdatePlayerLives(_ context.Context, id string, lives int, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lives[id] = lives
	return nil
}

func (s *recordingStore) UpdateGameStatus(_ context.Context, _ string, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, status)
	return nil
}

// A question closed at its answer deadline must be persisted like a manual close.
func TestAnswerTimeoutIsPersisted(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := &recordingStore{lives: map[string]int{}}
	manager := testManager(t, nopBroadcaster{})
	h := NewGameHandler(manager, fakeSessions{}, rec, rec, oneQuestionStore{}, game.EngineConfig{InitialLives: 3}, logger)

	body := `{"owner_name":"Alice","question_list_id":"list-1","answer_timeout_seconds":1}`
	w := httptest.NewRecorder()
	h.CreateGame(w, httptest.NewRequest(http.MethodPost, "/games", strings.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("create game: %d %s", w.Code, w.Body)
	}
	var created struct {
		GameID  string `json:"game_id"`
		OwnerID string `json:"owner_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)

	ctx := context.Background()
	eng, _ := manager.Get(ctx, created.GameID)
	if err := eng.StartNextQuestion(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Nobody answers and nobody closes: the deadline does.
	if n := manager.CloseDue(ctx, time.Now().Add(2*time.Second)); n != 1 {
		t.Fatalf("expected the deadline to close the question, closed %d", n)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if got, ok := rec.lives[created.OwnerID]; !ok || got != 2 {
		t.Fatalf("expected owner lives persisted as 2, got %d (recorded=%v)", got, ok)
	}
	if len(rec.statuses) != 1 || rec.statuses[0] != string(domain.GameStatusFinished) {
		t.Fatalf("expected finished status persisted, got %v", rec.statuses)
	}
}
