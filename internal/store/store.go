package store

import (
	"context"
	"time"
)

// GameRecord is the persistence model for a game (separate from the in-memory domain.Game).
type GameRecord struct {
	ID        string
	OwnerID   string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PlayerRecord is the persistence model for a player.
type PlayerRecord struct {
	ID        string
	GameID    string
	Name      string
	Lives     int
	Active    bool
	CreatedAt time.Time
}

// QuestionRecord is the persistence model for a question.
type QuestionRecord struct {
	ID              string
	GameID          string
	Text            string
	Options         []OptionRecord
	CorrectOptionID string
	Index           int
}

// OptionRecord represents a single answer option.
type OptionRecord struct {
	ID   string
	Text string
}

// GameStore handles game persistence.
type GameStore interface {
	CreateGame(ctx context.Context, g GameRecord) error
	GetGame(ctx context.Context, id string) (*GameRecord, error)
	UpdateGameStatus(ctx context.Context, id, status string) error
}

// PlayerStore handles player persistence.
type PlayerStore interface {
	CreatePlayer(ctx context.Context, p PlayerRecord) error
	GetPlayer(ctx context.Context, id string) (*PlayerRecord, error)
	ListPlayers(ctx context.Context, gameID string) ([]PlayerRecord, error)
	UpdatePlayerLives(ctx context.Context, id string, lives int, active bool) error
}

// QuestionStore handles question persistence.
type QuestionStore interface {
	CreateQuestion(ctx context.Context, q QuestionRecord) error
	ListQuestions(ctx context.Context, gameID string) ([]QuestionRecord, error)
}
