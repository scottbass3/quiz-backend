package game

import (
	"context"
	"time"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

// StateStore persists game state so that any backend instance can serve any
// game. It is implemented by redis.GameStateStore.
//
// Concurrency model:
//   - state transitions (join, start, close) run under Lock, which serializes
//     them per game across instances;
//   - answers are recorded without the lock: RecordAnswer checks that the
//     question is still open and stores the first answer atomically;
//   - CloseAnswers atomically marks the question closed and returns its
//     answers, so no answer can slip in between.
//
// Every write refreshes the expiry of the game's data: idle games expire
// after the store's idle TTL, finished games after its finished TTL.
type StateStore interface {
	// Create stores a new game (no players yet) and its questions.
	Create(ctx context.Context, s *State, questions []*domain.Question) error
	// Load returns the game with its players, its current question and the
	// answers to it. ErrGameNotFound if the game does not exist (or expired).
	Load(ctx context.Context, gameID string) (*State, error)
	// Question returns the question at index idx.
	Question(ctx context.Context, gameID string, idx int) (*domain.Question, error)
	// Exists reports whether the game still exists.
	Exists(ctx context.Context, gameID string) (bool, error)

	// Lock serializes state transitions of one game. It waits a bounded time
	// for the lock and returns ErrBusy if it cannot get it. The lock expires on
	// its own if its holder dies.
	Lock(ctx context.Context, gameID string) (unlock func(), err error)
	// AddPlayer stores a new player and indexes the game for the player's actor.
	AddPlayer(ctx context.Context, gameID string, gameCreatedAt time.Time, p *domain.Player) error
	// Save writes the game's progress fields (status, current question, end
	// reason) and the given players.
	Save(ctx context.Context, s *State, players ...*domain.Player) error

	// RecordAnswer stores the first answer of playerID to question idx if that
	// question is the open one. Errors: ErrGameNotFound, ErrGameNotRunning,
	// ErrNoActiveQuestion, ErrAlreadyAnswered.
	RecordAnswer(ctx context.Context, gameID string, idx int, playerID, optionID string) error
	// CloseAnswers marks question idx closed and returns its answers (player
	// ID to option ID). ErrNoActiveQuestion if it is not the open question.
	CloseAnswers(ctx context.Context, gameID string, idx int) (map[string]string, error)

	// ScheduleClose registers the answer deadline of question idx.
	ScheduleClose(ctx context.Context, gameID string, idx int, at time.Time) error
	// CancelClose forgets the deadline of question idx.
	CancelClose(ctx context.Context, gameID string, idx int) error
	// ClaimDueCloses removes and returns up to max deadlines reached at now.
	// Each deadline is returned to exactly one caller across instances.
	ClaimDueCloses(ctx context.Context, now time.Time, max int) ([]DueClose, error)

	// GamesOfActor returns the IDs of existing games where actorID owns a
	// player (the host owns the owner player), newest first.
	GamesOfActor(ctx context.Context, actorID string) ([]string, error)
}

// DueClose is a question whose answer deadline has passed.
type DueClose struct {
	GameID string
	Index  int
}
