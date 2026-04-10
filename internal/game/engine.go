package game

import (
	"errors"
	"sync"
	"time"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

var (
	ErrGameAlreadyStarted  = errors.New("game already started")
	ErrGameNotRunning      = errors.New("game is not running")
	ErrGameFinished        = errors.New("game is finished")
	ErrPlayerNotFound      = errors.New("player not found")
	ErrPlayerAlreadyJoined = errors.New("player already joined")
	ErrPlayerEliminated    = errors.New("player is eliminated")
	ErrAlreadyAnswered     = errors.New("player already answered this question")
	ErrNoMoreQuestions     = errors.New("no more questions")
	ErrNoActiveQuestion    = errors.New("no active question")
	ErrWrongQuestion       = errors.New("question id does not match active question")
)

type EngineConfig struct {
	InitialLives int
}

// Broadcaster is implemented by the WebSocket hub.
// The engine uses it to push events without importing the ws package directly.
type Broadcaster interface {
	Broadcast(event domain.Event)
	BroadcastTo(playerID string, event domain.Event)
}

type CloseQuestionResult struct {
	LifeLost  []string
	Eliminated []string
	GameOver  bool
	Winner    string // empty if draw / no survivors
}

type Engine struct {
	mu   sync.RWMutex
	game *domain.Game
	cfg  EngineConfig
	hub  Broadcaster
}

func NewEngine(gameID, ownerID string, cfg EngineConfig, hub Broadcaster) *Engine {
	return &Engine{
		game: &domain.Game{
			ID:          gameID,
			Status:      domain.GameStatusWaiting,
			OwnerID:     ownerID,
			Players:     make(map[string]*domain.Player),
			Questions:   make([]*domain.Question, 0),
			CurrentQIdx: -1,
			CreatedAt:   time.Now(),
		},
		cfg: cfg,
		hub: hub,
	}
}

func (e *Engine) AddPlayer(id, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.game.Status != domain.GameStatusWaiting {
		return ErrGameAlreadyStarted
	}
	if _, exists := e.game.Players[id]; exists {
		return ErrPlayerAlreadyJoined
	}

	e.game.Players[id] = &domain.Player{
		ID:     id,
		Name:   name,
		Lives:  e.cfg.InitialLives,
		Active: true,
		GameID: e.game.ID,
	}
	return nil
}

func (e *Engine) AddQuestion(q *domain.Question) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.game.Status == domain.GameStatusFinished {
		return ErrGameFinished
	}
	if q.Answers == nil {
		q.Answers = make(map[string]*domain.Answer)
	}
	e.game.Questions = append(e.game.Questions, q)
	return nil
}

// StartNextQuestion advances to the next question and broadcasts question_started.
func (e *Engine) StartNextQuestion() error {
	var event domain.Event

	e.mu.Lock()
	if e.game.Status == domain.GameStatusFinished {
		e.mu.Unlock()
		return ErrGameFinished
	}

	nextIdx := e.game.CurrentQIdx + 1
	if nextIdx >= len(e.game.Questions) {
		e.mu.Unlock()
		return ErrNoMoreQuestions
	}

	e.game.Status = domain.GameStatusRunning
	e.game.CurrentQIdx = nextIdx
	q := e.game.Questions[nextIdx]
	total := len(e.game.Questions)
	e.mu.Unlock()

	event = domain.Event{
		Type: domain.EventQuestionStarted,
		Payload: map[string]any{
			"question_id": q.ID,
			"index":       nextIdx,
			"total":       total,
			"text":        q.Text,
			"options":     q.Options,
		},
	}

	e.hub.Broadcast(event)
	return nil
}

// SubmitAnswer records a player's answer. Only the first submission counts.
func (e *Engine) SubmitAnswer(playerID, questionID, optionID string) error {
	var event domain.Event

	e.mu.Lock()
	if e.game.Status != domain.GameStatusRunning {
		e.mu.Unlock()
		return ErrGameNotRunning
	}
	player, ok := e.game.Players[playerID]
	if !ok {
		e.mu.Unlock()
		return ErrPlayerNotFound
	}
	if !player.Active {
		e.mu.Unlock()
		return ErrPlayerEliminated
	}
	if e.game.CurrentQIdx < 0 {
		e.mu.Unlock()
		return ErrNoActiveQuestion
	}
	q := e.game.Questions[e.game.CurrentQIdx]
	if q.ID != questionID {
		e.mu.Unlock()
		return ErrWrongQuestion
	}
	if _, answered := q.Answers[playerID]; answered {
		e.mu.Unlock()
		return ErrAlreadyAnswered
	}

	q.Answers[playerID] = &domain.Answer{
		PlayerID:    playerID,
		QuestionID:  questionID,
		OptionID:    optionID,
		Correct:     q.CorrectOptionID == optionID,
		SubmittedAt: time.Now(),
	}
	e.mu.Unlock()

	// Broadcast answer_submitted without revealing correctness.
	event = domain.Event{
		Type: domain.EventAnswerSubmitted,
		Payload: map[string]any{
			"player_id":   playerID,
			"question_id": questionID,
		},
	}
	e.hub.Broadcast(event)
	return nil
}

// CloseQuestion ends the current question, applies life penalties, and detects game over.
func (e *Engine) CloseQuestion() (*CloseQuestionResult, error) {
	e.mu.Lock()

	if e.game.Status != domain.GameStatusRunning {
		e.mu.Unlock()
		return nil, ErrGameNotRunning
	}
	if e.game.CurrentQIdx < 0 {
		e.mu.Unlock()
		return nil, ErrNoActiveQuestion
	}

	q := e.game.Questions[e.game.CurrentQIdx]
	result := &CloseQuestionResult{}

	for playerID, player := range e.game.Players {
		if !player.Active {
			continue
		}
		answer, answered := q.Answers[playerID]
		if !answered || !answer.Correct {
			player.Lives--
			result.LifeLost = append(result.LifeLost, playerID)
			if player.Lives <= 0 {
				player.Active = false
				result.Eliminated = append(result.Eliminated, playerID)
			}
		}
	}

	active := e.activePlayers()
	if len(active) <= 1 {
		e.game.Status = domain.GameStatusFinished
		result.GameOver = true
		if len(active) == 1 {
			result.Winner = active[0]
		}
	}

	// snapshot data needed for events before releasing lock
	type playerSnapshot struct {
		id    string
		lives int
	}
	livesAfter := make(map[string]int, len(result.LifeLost))
	for _, pid := range result.LifeLost {
		livesAfter[pid] = e.game.Players[pid].Lives
	}
	correctOptionID := q.CorrectOptionID
	questionID := q.ID
	winner := result.Winner
	gameOver := result.GameOver

	e.mu.Unlock()

	// Broadcast events after releasing the lock.
	e.hub.Broadcast(domain.Event{
		Type: domain.EventQuestionClosed,
		Payload: map[string]any{
			"question_id":       questionID,
			"correct_option_id": correctOptionID,
		},
	})
	for _, pid := range result.LifeLost {
		e.hub.BroadcastTo(pid, domain.Event{
			Type: domain.EventLifeLost,
			Payload: map[string]any{
				"player_id":  pid,
				"lives_left": livesAfter[pid],
			},
		})
	}
	for _, pid := range result.Eliminated {
		e.hub.Broadcast(domain.Event{
			Type: domain.EventPlayerEliminated,
			Payload: map[string]any{
				"player_id": pid,
			},
		})
	}
	if gameOver {
		e.hub.Broadcast(domain.Event{
			Type: domain.EventGameOver,
			Payload: map[string]any{
				"winner_id": winner,
			},
		})
	}

	return result, nil
}

// Snapshot returns a read-only view of the game state. Callers must not mutate.
func (e *Engine) Snapshot() domain.Game {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return *e.game
}

func (e *Engine) activePlayers() []string {
	// assumes lock is held
	active := make([]string, 0, len(e.game.Players))
	for id, p := range e.game.Players {
		if p.Active {
			active = append(active, id)
		}
	}
	return active
}
