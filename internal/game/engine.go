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
	ErrQuestionOpen        = errors.New("current question is still open")
)

type EngineConfig struct {
	InitialLives         int
	AnswerTimeoutSeconds int // 0 = no timeout
}

// Broadcaster is implemented by redis.PubSubBroadcaster in production and by
// ws.Hub directly. The engine uses it to push events without knowing the transport.
type Broadcaster interface {
	Broadcast(event domain.Event)
	BroadcastTo(playerID string, event domain.Event)
}

// LifeDelta records the life change for a single player after closing a question.
// Used for best-effort persistence to Postgres without needing a second snapshot.
type LifeDelta struct {
	PlayerID  string
	LivesLeft int
	Active    bool
}

type CloseQuestionResult struct {
	LifeLost   []string    // player IDs who lost a life
	LifeDeltas []LifeDelta // detailed life info for persistence
	Eliminated []string    // player IDs who reached 0 lives
	GameOver   bool
	Winner     string                // empty if draw / no survivors
	Survivors  []string              // active players when the game ended (set only if GameOver)
	Reason     domain.GameOverReason // why the game ended (set only if GameOver)

	// RemainingQuestions is the number of questions not played yet. It can be
	// non-zero on game over when the game ended by elimination.
	RemainingQuestions int
}

type Engine struct {
	mu   sync.RWMutex
	game *domain.Game
	cfg  EngineConfig
	hub  Broadcaster

	// lastActivity is the time of the last state change (join, question
	// started/closed, answer). Used by Manager.Sweep to evict stale games.
	lastActivity time.Time

	// answerTimer auto-closes the open question after AnswerTimeoutSeconds.
	// Armed on start, stopped on close. Guarded by mu.
	answerTimer *time.Timer

	// onQuestionClosed is called after every close, manual or by timeout.
	// Guarded by mu.
	onQuestionClosed func(*CloseQuestionResult)
}

// Config returns the per-game configuration the engine was created with.
func (e *Engine) Config() EngineConfig {
	return e.cfg // immutable after NewEngine
}

// OnQuestionClosed registers fn to be called after every question close,
// whether triggered by CloseQuestion or by the answer timeout. It runs on the
// closing goroutine after events are broadcast and the lock is released.
// Used for persistence, which the engine itself knows nothing about.
func (e *Engine) OnQuestionClosed(fn func(*CloseQuestionResult)) {
	e.mu.Lock()
	e.onQuestionClosed = fn
	e.mu.Unlock()
}

// NewEngine creates a new game engine.
// questions is the pre-loaded set from the question list (may be nil for tests).
func NewEngine(gameID, ownerID, questionListID string, questions []*domain.Question, cfg EngineConfig, hub Broadcaster) *Engine {
	if questions == nil {
		questions = make([]*domain.Question, 0)
	}
	// Ensure every question has an Answers map.
	for _, q := range questions {
		if q.Answers == nil {
			q.Answers = make(map[string]*domain.Answer)
		}
	}
	return &Engine{
		game: &domain.Game{
			ID:             gameID,
			Status:         domain.GameStatusWaiting,
			OwnerID:        ownerID,
			QuestionListID: questionListID,
			Players:        make(map[string]*domain.Player),
			Questions:      questions,
			CurrentQIdx:    -1,
			CreatedAt:      time.Now(),
		},
		cfg:          cfg,
		hub:          hub,
		lastActivity: time.Now(),
	}
}

// AddPlayer registers a player owned by actorID (the authenticated actor who
// joined). Only that actor may later act as this player over WebSocket.
// It broadcasts player_joined so lobbies can update without polling.
func (e *Engine) AddPlayer(id, name, actorID string) error {
	e.mu.Lock()
	if e.game.Status != domain.GameStatusWaiting {
		e.mu.Unlock()
		return ErrGameAlreadyStarted
	}
	if _, exists := e.game.Players[id]; exists {
		e.mu.Unlock()
		return ErrPlayerAlreadyJoined
	}

	e.game.Players[id] = &domain.Player{
		ID:      id,
		Name:    name,
		Lives:   e.cfg.InitialLives,
		Active:  true,
		GameID:  e.game.ID,
		ActorID: actorID,
	}
	lives := e.cfg.InitialLives
	e.lastActivity = time.Now()
	e.mu.Unlock()

	e.hub.Broadcast(domain.Event{
		Type: domain.EventPlayerJoined,
		Payload: map[string]any{
			"player_id": id,
			"name":      name,
			"lives":     lives,
		},
	})
	return nil
}

// HostActorID returns the actor who owns the game's owner player, i.e. the
// actor allowed to start and close questions. Empty until the owner is added.
func (e *Engine) HostActorID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if owner, ok := e.game.Players[e.game.OwnerID]; ok {
		return owner.ActorID
	}
	return ""
}

// PlayerActorID returns the actor that owns playerID.
func (e *Engine) PlayerActorID(playerID string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.game.Players[playerID]
	if !ok {
		return "", false
	}
	return p.ActorID, true
}

// AddQuestion appends a question to the engine's runtime question list.
// Useful in tests; in production, questions are loaded from the list at creation.
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
		reason := e.game.EndReason
		e.mu.Unlock()
		if reason == domain.GameOverNoMoreQuestions {
			return ErrNoMoreQuestions
		}
		return ErrGameFinished
	}
	if e.game.QuestionOpen {
		e.mu.Unlock()
		return ErrQuestionOpen
	}

	nextIdx := e.game.CurrentQIdx + 1
	if nextIdx >= len(e.game.Questions) {
		e.mu.Unlock()
		return ErrNoMoreQuestions
	}

	e.game.Status = domain.GameStatusRunning
	e.game.CurrentQIdx = nextIdx
	e.game.QuestionOpen = true
	q := e.game.Questions[nextIdx]
	total := len(e.game.Questions)
	timeout := e.cfg.AnswerTimeoutSeconds
	if timeout > 0 {
		// Armed under the lock so a close racing with this start can stop it.
		e.answerTimer = time.AfterFunc(time.Duration(timeout)*time.Second, func() {
			e.closeQuestion(nextIdx) //nolint:errcheck // stale timers are expected to fail
		})
	}
	e.lastActivity = time.Now()
	e.mu.Unlock()

	payload := map[string]any{
		"question_id": q.ID,
		"index":       nextIdx,
		"total":       total,
		"is_last":     nextIdx == total-1,
		"text":        q.Text,
		"options":     q.Options,
		"theme":       q.Theme, // null when the question has no theme
	}
	if timeout > 0 {
		payload["answer_timeout_seconds"] = timeout
	}
	event = domain.Event{Type: domain.EventQuestionStarted, Payload: payload}

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
	if !e.game.QuestionOpen {
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
	e.lastActivity = time.Now()
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
	return e.closeQuestion(-1)
}

// closeQuestion closes the open question. When onlyIdx >= 0 it closes only if
// that question is the open one, so a timer armed for an earlier question can
// never close a later one. The check and the close happen under one lock.
func (e *Engine) closeQuestion(onlyIdx int) (*CloseQuestionResult, error) {
	e.mu.Lock()

	if e.game.Status != domain.GameStatusRunning {
		e.mu.Unlock()
		return nil, ErrGameNotRunning
	}
	if !e.game.QuestionOpen || (onlyIdx >= 0 && e.game.CurrentQIdx != onlyIdx) {
		e.mu.Unlock()
		return nil, ErrNoActiveQuestion
	}

	e.game.QuestionOpen = false
	if e.answerTimer != nil {
		e.answerTimer.Stop()
		e.answerTimer = nil
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

	// The game ends when at most one player is left, or when the last question
	// has been played. In the latter case the survivor with the most lives wins;
	// a tie on lives is a draw (no winner).
	active := e.activePlayers()
	result.RemainingQuestions = len(e.game.Questions) - e.game.CurrentQIdx - 1
	switch {
	case len(active) == 1:
		result.Reason = domain.GameOverLastPlayerStanding
	case len(active) == 0:
		result.Reason = domain.GameOverAllEliminated
	case result.RemainingQuestions == 0:
		result.Reason = domain.GameOverNoMoreQuestions
	}
	if result.Reason != "" {
		e.game.Status = domain.GameStatusFinished
		e.game.EndReason = result.Reason
		result.GameOver = true
		result.Survivors = active
		result.Winner = e.leaderLocked(active)
	}

	// Snapshot data needed for events and persistence before releasing the lock.
	livesAfter := make(map[string]int, len(result.LifeLost))
	for _, pid := range result.LifeLost {
		livesAfter[pid] = e.game.Players[pid].Lives
	}
	// Build LifeDeltas for best-effort Postgres persistence.
	result.LifeDeltas = make([]LifeDelta, 0, len(result.LifeLost))
	for _, pid := range result.LifeLost {
		p := e.game.Players[pid]
		result.LifeDeltas = append(result.LifeDeltas, LifeDelta{
			PlayerID:  pid,
			LivesLeft: p.Lives,
			Active:    p.Active,
		})
	}

	correctOptionID := q.CorrectOptionID
	questionID := q.ID
	winner := result.Winner
	gameOver := result.GameOver

	e.lastActivity = time.Now()
	onClose := e.onQuestionClosed
	e.mu.Unlock()

	// Broadcast events after releasing the lock.
	e.hub.Broadcast(domain.Event{
		Type: domain.EventQuestionClosed,
		Payload: map[string]any{
			"question_id":         questionID,
			"correct_option_id":   correctOptionID,
			"remaining_questions": result.RemainingQuestions,
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
				"reason":    result.Reason,
				"winner_id": winner,
				"survivors": result.Survivors,
			},
		})
	}

	if onClose != nil {
		onClose(result)
	}
	return result, nil
}

// Snapshot returns a deep copy of the game state, safe to read after the lock
// is released. Option slices are shared because they are never mutated.
func (e *Engine) Snapshot() domain.Game {
	e.mu.RLock()
	defer e.mu.RUnlock()

	g := *e.game
	g.Players = make(map[string]*domain.Player, len(e.game.Players))
	for id, p := range e.game.Players {
		cp := *p
		g.Players[id] = &cp
	}
	g.Questions = make([]*domain.Question, len(e.game.Questions))
	for i, q := range e.game.Questions {
		cq := *q
		cq.Answers = make(map[string]*domain.Answer, len(q.Answers))
		for pid, a := range q.Answers {
			ca := *a
			cq.Answers[pid] = &ca
		}
		g.Questions[i] = &cq
	}
	return g
}

// Expired reports whether the game can be evicted at now: finished games are
// kept for finishedTTL after their last question (so clients can still read
// the final state), unfinished games are evicted after idleTTL without activity.
func (e *Engine) Expired(now time.Time, finishedTTL, idleTTL time.Duration) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	idle := now.Sub(e.lastActivity)
	if e.game.Status == domain.GameStatusFinished {
		return idle >= finishedTTL
	}
	return idle >= idleTTL
}

// leaderLocked returns the player with strictly the most lives among ids,
// or "" if ids is empty or the top is tied. Caller must hold the lock.
func (e *Engine) leaderLocked(ids []string) string {
	leader, best, tied := "", -1, false
	for _, id := range ids {
		switch lives := e.game.Players[id].Lives; {
		case lives > best:
			leader, best, tied = id, lives, false
		case lives == best:
			tied = true
		}
	}
	if tied {
		return ""
	}
	return leader
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
