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
	"testing"

	"github.com/scottbass3/quizz-backend/internal/game"
	"github.com/scottbass3/quizz-backend/internal/store"
)

func TestWriteGameError(t *testing.T) {
	cases := map[error]string{
		game.ErrNoMoreQuestions:  "no_more_questions",
		game.ErrGameFinished:     "game_finished",
		game.ErrGameNotRunning:   "game_not_running",
		game.ErrNoActiveQuestion: "no_active_question",
		errors.New("other"):      "conflict",
	}
	for err, want := range cases {
		rec := httptest.NewRecorder()
		writeGameError(rec, err)

		var body map[string]string
		json.NewDecoder(rec.Body).Decode(&body)
		if rec.Code != http.StatusConflict || body["code"] != want || body["error"] != err.Error() {
			t.Errorf("%v: got %d %v, want 409 code=%s", err, rec.Code, body, want)
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
	h := NewGameHandler(game.NewManager(cfg), nil, nil, nil, emptyListStore{}, cfg, logger)

	req := httptest.NewRequest(http.MethodPost, "/games", strings.NewReader(`{"owner_name":"Alice","question_list_id":"list-1"}`))
	rec := httptest.NewRecorder()
	h.CreateGame(rec, req)

	var body map[string]string
	json.NewDecoder(rec.Body).Decode(&body)
	if rec.Code != http.StatusBadRequest || body["code"] != "empty_question_list" {
		t.Fatalf("expected 400 empty_question_list, got %d %v", rec.Code, body)
	}
}
