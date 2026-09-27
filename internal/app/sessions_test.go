package app

import (
	"io"
	"log/slog"
	"testing"

	"github.com/alicebob/miniredis/v2"
	appredis "github.com/scottbass3/quizz-backend/internal/redis"
)

func TestGameSessionStore_RefCounting(t *testing.T) {
	mr := miniredis.RunT(t)
	s := newGameSessionStore(appredis.New(mr.Addr(), ""), slog.New(slog.NewTextHandler(io.Discard, nil)))

	h1, err := s.Acquire("g1")
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := s.Acquire("g1")
	if h1 != h2 {
		t.Fatal("clients of the same game must share the local hub")
	}
	if got := mr.PubSubNumSub("game:g1:events")["game:g1:events"]; got != 1 {
		t.Fatalf("expected one Redis subscription, got %d", got)
	}

	s.Release("g1")
	if len(s.GameIDs()) != 1 {
		t.Fatal("session must stay while a client is connected")
	}
	s.Release("g1")
	if len(s.GameIDs()) != 0 {
		t.Fatal("last release must remove the session")
	}

	h3, _ := s.Acquire("g1")
	if h3 == h1 {
		t.Fatal("a new session must be created after the last release")
	}
	s.Release("g1")
}
