package game

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

var (
	ErrGameNotFound        = errors.New("game not found")
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
	ErrInvalidOption       = errors.New("option id does not match any option of the question")
	ErrBusy                = errors.New("game is busy, retry")
)

type EngineConfig struct {
	InitialLives         int
	AnswerTimeoutSeconds int // 0 = no timeout
}

// Broadcaster delivers game events to players. It is implemented by
// redis.GamePublisher in production (events reach every instance) and by
// ws.Hub directly in tests. The engine does not know the transport.
type Broadcaster interface {
	Broadcast(event domain.Event)
	BroadcastTo(playerID string, event domain.Event)
}

// LifeDelta records the life change for a single player after closing a question.
// Used for best-effort persistence to Postgres.
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

	// Players is the scoreboard after this question, sorted by name.
	Players []PlayerScore
}

// Engine applies the game rules to one game. It holds no state itself: every
// operation reads and writes the StateStore, so any instance can serve any
// game. The critical rule is unchanged: events are broadcast after the
// transition is stored and the lock is released.
type Engine struct {
	m   *Manager
	id  string
	hub Broadcaster
}

// ID returns the game ID.
func (e *Engine) ID() string { return e.id }

// State loads the current state of the game.
func (e *Engine) State(ctx context.Context) (*State, error) {
	return e.m.store.Load(ctx, e.id)
}

// locked runs fn with the game's lock held and the state loaded under it.
func (e *Engine) locked(ctx context.Context, fn func(s *State) error) error {
	unlock, err := e.m.store.Lock(ctx, e.id)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := e.m.store.Load(ctx, e.id)
	if err != nil {
		return err
	}
	return fn(s)
}

// AddPlayer registers a player owned by actorID (the authenticated actor who
// joined). Only that actor may later act as this player over WebSocket.
// It broadcasts player_joined so lobbies can update without polling.
func (e *Engine) AddPlayer(ctx context.Context, id, name, actorID string) error {
	var lives int
	err := e.locked(ctx, func(s *State) error {
		if s.Status != domain.GameStatusWaiting {
			return ErrGameAlreadyStarted
		}
		if _, exists := s.Players[id]; exists {
			return ErrPlayerAlreadyJoined
		}
		lives = s.Config.InitialLives
		return e.m.store.AddPlayer(ctx, e.id, s.CreatedAt, &domain.Player{
			ID:      id,
			Name:    name,
			Lives:   lives,
			Active:  true,
			GameID:  e.id,
			ActorID: actorID,
		})
	})
	if err != nil {
		return err
	}

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

// StartNextQuestion advances to the next question and broadcasts question_started.
// With an answer timeout, it registers the deadline so that any instance can
// close the question when it passes (see Manager.CloseDue).
func (e *Engine) StartNextQuestion(ctx context.Context) error {
	var payload map[string]any
	err := e.locked(ctx, func(s *State) error {
		if s.Status == domain.GameStatusFinished {
			if s.EndReason == domain.GameOverNoMoreQuestions {
				return ErrNoMoreQuestions
			}
			return ErrGameFinished
		}
		if s.QuestionOpen {
			return ErrQuestionOpen
		}
		next := s.CurrentQIdx + 1
		if next >= s.TotalQuestions {
			return ErrNoMoreQuestions
		}
		q, err := e.m.store.Question(ctx, e.id, next)
		if err != nil {
			return err
		}

		s.Status = domain.GameStatusRunning
		s.CurrentQIdx = next
		s.QuestionOpen = true
		s.QuestionStart = time.Now()
		s.Current = q
		s.Answers = map[string]string{}

		// Schedule before saving: if the save fails, the stale deadline is
		// harmless (closing checks the question index).
		if t := s.Config.AnswerTimeoutSeconds; t > 0 {
			deadline := s.QuestionStart.Add(time.Duration(t) * time.Second)
			if err := e.m.store.ScheduleClose(ctx, e.id, next, deadline); err != nil {
				return err
			}
		}
		if err := e.m.store.Save(ctx, s); err != nil {
			return err
		}
		payload = s.questionPayload()
		return nil
	})
	if err != nil {
		return err
	}

	e.hub.Broadcast(domain.Event{Type: domain.EventQuestionStarted, Payload: payload})
	return nil
}

// SubmitAnswer records a player's answer. Only the first submission counts.
// It does not take the game lock: the store records the answer atomically,
// and only if the question is still the open one.
func (e *Engine) SubmitAnswer(ctx context.Context, playerID, questionID, optionID string) error {
	s, err := e.m.store.Load(ctx, e.id)
	if err != nil {
		return err
	}
	if s.Status != domain.GameStatusRunning {
		return ErrGameNotRunning
	}
	player, ok := s.Players[playerID]
	if !ok {
		return ErrPlayerNotFound
	}
	if !player.Active {
		return ErrPlayerEliminated
	}
	if !s.QuestionOpen || s.Current == nil {
		return ErrNoActiveQuestion
	}
	if s.Current.ID != questionID {
		return ErrWrongQuestion
	}
	if _, answered := s.Answers[playerID]; answered {
		return ErrAlreadyAnswered
	}
	if !hasOption(s.Current, optionID) {
		return ErrInvalidOption
	}
	if err := e.m.store.RecordAnswer(ctx, e.id, s.CurrentQIdx, playerID, optionID); err != nil {
		return err
	}

	// Broadcast answer_submitted without revealing correctness.
	e.hub.Broadcast(domain.Event{
		Type: domain.EventAnswerSubmitted,
		Payload: map[string]any{
			"player_id":   playerID,
			"question_id": questionID,
		},
	})
	return nil
}

// CloseQuestion ends the current question, applies life penalties, and detects game over.
func (e *Engine) CloseQuestion(ctx context.Context) (*CloseQuestionResult, error) {
	return e.closeQuestion(ctx, -1)
}

// closeQuestion closes the open question. When onlyIdx >= 0 it closes only
// if that question is the open one, so a deadline registered for an earlier
// question can never close a later one.
func (e *Engine) closeQuestion(ctx context.Context, onlyIdx int) (*CloseQuestionResult, error) {
	var (
		result          *CloseQuestionResult
		questionID      string
		correctOptionID string
		livesAfter      map[string]int
	)
	err := e.locked(ctx, func(s *State) error {
		if s.Status != domain.GameStatusRunning {
			return ErrGameNotRunning
		}
		if !s.QuestionOpen || (onlyIdx >= 0 && s.CurrentQIdx != onlyIdx) {
			return ErrNoActiveQuestion
		}
		// Closing and reading the answers is one atomic step: an answer is
		// either counted here or rejected as too late.
		answers, err := e.m.store.CloseAnswers(ctx, e.id, s.CurrentQIdx)
		if err != nil {
			return err
		}
		s.QuestionOpen = false
		s.Answers = answers
		result, livesAfter = applyPenalties(s)

		changed := make([]*domain.Player, 0, len(result.LifeLost))
		for _, pid := range result.LifeLost {
			changed = append(changed, s.Players[pid])
		}
		if err := e.m.store.Save(ctx, s, changed...); err != nil {
			return err
		}
		if err := e.m.store.CancelClose(ctx, e.id, s.CurrentQIdx); err != nil {
			e.m.logger.Warn("cancel answer deadline", "error", err, "game_id", e.id)
		}
		questionID, correctOptionID = s.Current.ID, s.Current.CorrectOptionID
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Broadcast events after the lock is released.
	e.hub.Broadcast(domain.Event{
		Type: domain.EventQuestionClosed,
		Payload: map[string]any{
			"question_id":         questionID,
			"correct_option_id":   correctOptionID,
			"remaining_questions": result.RemainingQuestions,
			"players":             result.Players,
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
			Type:    domain.EventPlayerEliminated,
			Payload: map[string]any{"player_id": pid},
		})
	}
	if result.GameOver {
		e.hub.Broadcast(domain.Event{
			Type: domain.EventGameOver,
			Payload: map[string]any{
				"reason":    result.Reason,
				"winner_id": result.Winner,
				"survivors": result.Survivors,
			},
		})
	}

	e.m.questionClosed(e.id, result)
	return result, nil
}

// applyPenalties applies the rules of a closed question to s: every active
// player who answered wrong or not at all loses a life, 0 lives eliminates.
// The game ends when at most one player is left, or after the last question;
// then the survivor with the most lives wins and a tie is a draw.
func applyPenalties(s *State) (*CloseQuestionResult, map[string]int) {
	result := &CloseQuestionResult{}
	livesAfter := map[string]int{}
	for _, pid := range sortedPlayerIDs(s) {
		player := s.Players[pid]
		if !player.Active {
			continue
		}
		if s.Answers[pid] != s.Current.CorrectOptionID {
			player.Lives--
			result.LifeLost = append(result.LifeLost, pid)
			if player.Lives <= 0 {
				player.Active = false
				result.Eliminated = append(result.Eliminated, pid)
			}
			livesAfter[pid] = player.Lives
			result.LifeDeltas = append(result.LifeDeltas, LifeDelta{PlayerID: pid, LivesLeft: player.Lives, Active: player.Active})
		}
	}

	active := s.activePlayers()
	result.RemainingQuestions = s.RemainingQuestions()
	switch {
	case len(active) == 1:
		result.Reason = domain.GameOverLastPlayerStanding
	case len(active) == 0:
		result.Reason = domain.GameOverAllEliminated
	case result.RemainingQuestions == 0:
		result.Reason = domain.GameOverNoMoreQuestions
	}
	if result.Reason != "" {
		s.Status = domain.GameStatusFinished
		s.EndReason = result.Reason
		result.GameOver = true
		result.Survivors = active
		result.Winner = s.leader(active)
	}
	result.Players = s.Scoreboard()
	return result, livesAfter
}

func sortedPlayerIDs(s *State) []string {
	ids := make([]string, 0, len(s.Players))
	for id := range s.Players {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
