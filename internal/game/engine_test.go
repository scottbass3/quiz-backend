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
	return game.NewEngine("game-1", "owner-1", "", nil, game.EngineConfig{InitialLives: 3}, hub)
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

	if err := eng.AddPlayer("p1", "Alice", "actor-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// duplicate
	if err := eng.AddPlayer("p1", "Alice", "actor-1"); err != game.ErrPlayerAlreadyJoined {
		t.Fatalf("expected ErrPlayerAlreadyJoined, got %v", err)
	}
}

func TestStartNextQuestion(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)

	eng.AddPlayer("p1", "Alice", "actor-1")

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

func TestStartNextQuestion_PreloadedQuestions(t *testing.T) {
	hub := newStubHub()
	q := sampleQuestion()
	eng := game.NewEngine("game-x", "owner-1", "list-1", []*domain.Question{q}, game.EngineConfig{InitialLives: 3}, hub)
	eng.AddPlayer("p1", "Alice", "actor-1")

	if err := eng.StartNextQuestion(); err != nil {
		t.Fatalf("unexpected error with preloaded questions: %v", err)
	}

	snap := eng.Snapshot()
	if snap.QuestionListID != "list-1" {
		t.Fatalf("expected QuestionListID 'list-1', got %q", snap.QuestionListID)
	}
}

func TestSubmitAnswer_CorrectThenClose(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)

	eng.AddPlayer("p1", "Alice", "actor-1")
	eng.AddPlayer("p2", "Bob", "actor-2")
	eng.AddQuestion(sampleQuestion())
	q2 := sampleQuestion()
	q2.ID = "q2"
	eng.AddQuestion(q2) // a second question keeps the game running after close
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
	eng := game.NewEngine("game-2", "owner-1", "", nil, game.EngineConfig{InitialLives: 1}, hub)

	eng.AddPlayer("p1", "Alice", "actor-1")
	eng.AddPlayer("p2", "Bob", "actor-2")

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
		eng.AddPlayer(string(rune('a'+i)), "player", "actor-1")
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

// Snapshot must be safe to read while the engine keeps mutating state
// (e.g. GET /games/{id} iterating players during a concurrent join).
func TestSnapshotIsolatedFromConcurrentMutations(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)
	eng.AddQuestion(sampleQuestion())

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			eng.AddPlayer(string(rune('a'+i)), "player", "actor-1")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			snap := eng.Snapshot()
			for _, p := range snap.Players {
				_ = p.Lives
			}
			for _, q := range snap.Questions {
				_ = len(q.Answers)
			}
		}
	}()
	wg.Wait()

	snap := eng.Snapshot()
	snap.Players["a"].Lives = 0
	if eng.Snapshot().Players["a"].Lives != 3 {
		t.Fatal("mutating a snapshot must not affect engine state")
	}
}

func TestGameEndsAfterLastQuestion_LeaderWins(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)
	eng.AddPlayer("p1", "Alice", "actor-1")
	eng.AddPlayer("p2", "Bob", "actor-2")
	eng.AddQuestion(sampleQuestion())
	eng.StartNextQuestion()

	// p2 answers wrong but keeps 2 lives: nobody is eliminated.
	eng.SubmitAnswer("p1", "q1", "b")
	eng.SubmitAnswer("p2", "q1", "a")
	result, err := eng.CloseQuestion()
	if err != nil {
		t.Fatalf("close question error: %v", err)
	}

	if !result.GameOver {
		t.Fatal("game should be over after the last question")
	}
	if result.Winner != "p1" {
		t.Fatalf("expected p1 (most lives) to win, got %q", result.Winner)
	}
	if len(result.Survivors) != 2 {
		t.Fatalf("expected 2 survivors, got %v", result.Survivors)
	}
	if eng.Snapshot().Status != domain.GameStatusFinished {
		t.Fatal("game status should be finished")
	}
	if err := eng.StartNextQuestion(); err != game.ErrGameFinished {
		t.Fatalf("expected ErrGameFinished, got %v", err)
	}
	types := hub.broadcastTypes()
	if types[len(types)-1] != domain.EventGameOver {
		t.Fatalf("expected game_over as last event, got %v", types)
	}
}

func TestGameEndsAfterLastQuestion_TieIsDraw(t *testing.T) {
	hub := newStubHub()
	eng := newEngine(hub)
	eng.AddPlayer("p1", "Alice", "actor-1")
	eng.AddPlayer("p2", "Bob", "actor-2")
	eng.AddQuestion(sampleQuestion())
	eng.StartNextQuestion()

	eng.SubmitAnswer("p1", "q1", "b")
	eng.SubmitAnswer("p2", "q1", "b")
	result, err := eng.CloseQuestion()
	if err != nil {
		t.Fatalf("close question error: %v", err)
	}

	if !result.GameOver {
		t.Fatal("game should be over after the last question")
	}
	if result.Winner != "" {
		t.Fatalf("expected a draw, got winner %q", result.Winner)
	}
	if len(result.Survivors) != 2 {
		t.Fatalf("expected 2 survivors, got %v", result.Survivors)
	}
}

func TestActorOwnership(t *testing.T) {
	eng := newEngine(newStubHub()) // owner player id is "owner-1"

	if got := eng.HostActorID(); got != "" {
		t.Fatalf("expected no host before the owner joins, got %q", got)
	}

	eng.AddPlayer("owner-1", "Host", "actor-host")
	eng.AddPlayer("p2", "Bob", "actor-bob")

	if got := eng.HostActorID(); got != "actor-host" {
		t.Fatalf("expected host actor-host, got %q", got)
	}
	if got, ok := eng.PlayerActorID("p2"); !ok || got != "actor-bob" {
		t.Fatalf("expected p2 owned by actor-bob, got %q (found=%v)", got, ok)
	}
	if _, ok := eng.PlayerActorID("unknown"); ok {
		t.Fatal("unknown player must not be found")
	}
}
