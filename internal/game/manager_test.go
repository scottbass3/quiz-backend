package game_test

import (
	"testing"
	"time"

	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
)

func TestManagerSweep(t *testing.T) {
	const finishedTTL, idleTTL = 10 * time.Minute, 2 * time.Hour
	cfg := game.EngineConfig{InitialLives: 3}
	m := game.NewManager()

	// A finished game: one question, played and closed.
	finished := m.Create("finished", "owner", "", []*domain.Question{sampleQuestion()}, cfg, newStubHub())
	finished.AddPlayer("owner", "Host", "actor-1")
	finished.StartNextQuestion()
	if res, err := finished.CloseQuestion(); err != nil || !res.GameOver {
		t.Fatalf("setup: expected finished game, got %+v, %v", res, err)
	}

	// A game still waiting for players.
	m.Create("waiting", "owner", "", []*domain.Question{sampleQuestion()}, cfg, newStubHub())

	now := time.Now()

	if removed := m.Sweep(now, finishedTTL, idleTTL); len(removed) != 0 {
		t.Fatalf("nothing should expire yet, removed %v", removed)
	}

	removed := m.Sweep(now.Add(finishedTTL+time.Second), finishedTTL, idleTTL)
	if len(removed) != 1 || removed[0] != "finished" {
		t.Fatalf("expected only the finished game to be evicted, got %v", removed)
	}
	if _, err := m.Get("finished"); err != game.ErrGameNotFound {
		t.Fatalf("finished game should be gone, got %v", err)
	}

	removed = m.Sweep(now.Add(idleTTL+time.Second), finishedTTL, idleTTL)
	if len(removed) != 1 || removed[0] != "waiting" {
		t.Fatalf("expected the idle game to be evicted, got %v", removed)
	}
	if m.Count() != 0 {
		t.Fatalf("expected no games left, got %d", m.Count())
	}
}
