package game_test

import (
	"sync"
	"testing"

	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
)

// stubHub captures broadcast calls for assertions.
type stubHub struct {
	mu     sync.Mutex
	events []domain.Event
	direct map[string][]domain.Event
}

func newStubHub() *stubHub {
	return &stubHub{direct: make(map[string][]domain.Event)}
}

func (s *stubHub) Broadcast(e domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *stubHub) BroadcastTo(playerID string, e domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.direct[playerID] = append(s.direct[playerID], e)
}

func (s *stubHub) broadcastTypes() []domain.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	types := make([]domain.EventType, len(s.events))
	for i, e := range s.events {
		types[i] = e.Type
	}
	return types
}

func (s *stubHub) directTypes(playerID string) []domain.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.direct[playerID]
	types := make([]domain.EventType, len(evs))
	for i, e := range evs {
		types[i] = e.Type
	}
	return types
}

func newEngine(hub game.Broadcaster) *game.Engine {
	return game.NewEngine("game-1", "owner-1", game.EngineConfig{InitialLives: 3}, hub)
}

func sampleQuestion() *domain.Question {
	return &domain.Question{
		ID:   "q1",
		Text: "What is 2+2?",
		Options: []domain.Option{
			{ID: "a", Text: "3"},
			{ID: "b", Text: "4"},
			{ID: "c", Text: "5"},
		},
		CorrectOptionID: "b",
		Answers:         make(map[string]*domain.Answer),
	}
}

func TestAddPlayer(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)

	if err := eng.AddPlayer("p1", "Alice"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// duplicate
	if err := eng.AddPlayer("p1", "Alice"); err != game.ErrPlayerAlreadyJoined {
		t.Fatalf("expected ErrPlayerAlreadyJoined, got %v", err)
	}
}

func TestStartNextQuestion(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)

	eng.AddPlayer("p1", "Alice")

	if err := eng.StartNextQuestion(); err != game.ErrNoMoreQuestions {
		t.Fatalf("expected ErrNoMoreQuestions, got %v", err)
	}

	eng.AddQuestion(sampleQuestion())

	if err := eng.StartNextQuestion(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	types := hub.broadcastTypes()
	if len(types) != 1 || types[0] != domain.EventQuestionStarted {
		t.Fatalf("expected question_started event, got %v", types)
	}
}

func TestSubmitAnswer_CorrectThenClose(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)

	eng.AddPlayer("p1", "Alice")
	eng.AddPlayer("p2", "Bob")
	eng.AddQuestion(sampleQuestion())
	eng.StartNextQuestion()
	hub.events = nil // reset after question_started

	// p1 answers correctly, p2 does not
	if err := eng.SubmitAnswer("p1", "q1", "b"); err != nil {
		t.Fatalf("p1 answer error: %v", err)
	}

	// duplicate answer must be rejected
	if err := eng.SubmitAnswer("p1", "q1", "b"); err != game.ErrAlreadyAnswered {
		t.Fatalf("expected ErrAlreadyAnswered, got %v", err)
	}

	result, err := eng.CloseQuestion()
	if err != nil {
		t.Fatalf("close question error: %v", err)
	}

	// p2 did not answer → loses a life
	if len(result.LifeLost) != 1 || result.LifeLost[0] != "p2" {
		t.Fatalf("expected p2 to lose a life, got %v", result.LifeLost)
	}
	if result.GameOver {
		t.Fatal("game should not be over yet")
	}

	snap := eng.Snapshot()
	if snap.Players["p1"].Lives != 3 {
		t.Fatalf("p1 lives should be 3, got %d", snap.Players["p1"].Lives)
	}
	if snap.Players["p2"].Lives != 2 {
		t.Fatalf("p2 lives should be 2, got %d", snap.Players["p2"].Lives)
	}
}

func TestPlayerEliminated(t *testing.T) {
	hub := newStubHub()
	eng := game.NewEngine("game-2", "owner-1", game.EngineConfig{InitialLives: 1}, hub)

	eng.AddPlayer("p1", "Alice")
	eng.AddPlayer("p2", "Bob")

	q := sampleQuestion()
	eng.AddQuestion(q)
	eng.StartNextQuestion()

	// p1 answers correctly, p2 does not → p2 eliminated (1 life)
	eng.SubmitAnswer("p1", "q1", "b")
	result, err := eng.CloseQuestion()
	if err != nil {
		t.Fatalf("close question error: %v", err)
	}

	if len(result.Eliminated) != 1 || result.Eliminated[0] != "p2" {
		t.Fatalf("expected p2 eliminated, got %v", result.Eliminated)
	}
	if !result.GameOver {
		t.Fatal("game should be over")
	}
	if result.Winner != "p1" {
		t.Fatalf("expected p1 as winner, got %q", result.Winner)
	}
}

func TestConcurrentSubmitAnswer(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)

	for i := 0; i < 50; i++ {
		eng.AddPlayer(string(rune('a'+i)), "player")
	}
	eng.AddQuestion(sampleQuestion())
	eng.StartNextQuestion()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			eng.SubmitAnswer(string(rune('a'+idx)), "q1", "b")
		}(i)
	}
	wg.Wait()

	snap := eng.Snapshot()
	q := snap.Questions[0]
	if len(q.Answers) != 50 {
		t.Fatalf("expected 50 answers, got %d", len(q.Answers))
	}
}
