package game

import (
	"testing"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

type nopHub struct{}

func (nopHub) Broadcast(domain.Event)           {}
func (nopHub) BroadcastTo(string, domain.Event) {}

func twoQuestions() []*domain.Question {
	return []*domain.Question{
		{ID: "q1", CorrectOptionID: "a", Options: []domain.Option{{ID: "a"}, {ID: "b"}}},
		{ID: "q2", CorrectOptionID: "a", Options: []domain.Option{{ID: "a"}, {ID: "b"}}},
	}
}

// A timer armed for question 0 that fires after the host closed it and
// started question 1 must leave question 1 open.
func TestStaleTimerDoesNotCloseNextQuestion(t *testing.T) {
	e := NewEngine("g", "owner", "", twoQuestions(), EngineConfig{InitialLives: 3}, nopHub{})
	e.AddPlayer("p1", "Alice", "actor-1")
	e.AddPlayer("p2", "Bob", "actor-2")

	e.StartNextQuestion()
	e.CloseQuestion()
	e.StartNextQuestion()

	if _, err := e.closeQuestion(0); err != ErrNoActiveQuestion {
		t.Fatalf("stale close: expected ErrNoActiveQuestion, got %v", err)
	}
	snap := e.Snapshot()
	if !snap.QuestionOpen || snap.CurrentQIdx != 1 {
		t.Fatalf("question 1 must still be open, got open=%v idx=%d", snap.QuestionOpen, snap.CurrentQIdx)
	}
}

func TestManualCloseStopsAnswerTimer(t *testing.T) {
	e := NewEngine("g", "owner", "", twoQuestions(), EngineConfig{InitialLives: 3, AnswerTimeoutSeconds: 60}, nopHub{})
	e.AddPlayer("p1", "Alice", "actor-1")
	e.AddPlayer("p2", "Bob", "actor-2")

	e.StartNextQuestion()
	if e.answerTimer == nil {
		t.Fatal("expected an armed answer timer")
	}
	timer := e.answerTimer

	e.CloseQuestion()
	if e.answerTimer != nil {
		t.Fatal("answer timer should be cleared on close")
	}
	if timer.Stop() {
		t.Fatal("answer timer should already be stopped")
	}
}
