package store

import (
	"context"
	"time"
)

// GameRecord is the persistence model for a game.
type GameRecord struct {
	ID             string
	OwnerID        string
	QuestionListID string // may be empty for games created without a list
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
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

// QuestionListRecord is the persistence model for a question list.
type QuestionListRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Visibility  string    `json:"visibility"` // "public" | "private"
	OwnerType   string    `json:"owner_type"` // "admin" | "user"
	OwnerID     string    `json:"owner_id"`   // empty for admin-created public lists
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// QuestionRecord is the persistence model for a catalog question (belongs to a question list).
type QuestionRecord struct {
	ID              string         `json:"id"`
	QuestionListID  string         `json:"question_list_id"`
	Text            string         `json:"text"`
	Options         []OptionRecord `json:"options"`
	CorrectOptionID string         `json:"correct_option_id"`
	OrderIndex      int            `json:"order_index"`
}

// OptionRecord represents a single answer option.
type OptionRecord struct {
	ID   string `json:"id"`
	Text string `json:"text"`
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

// QuestionListStore handles question list catalog operations.
type QuestionListStore interface {
	CreateQuestionList(ctx context.Context, l QuestionListRecord) error
	GetQuestionList(ctx context.Context, id string) (*QuestionListRecord, error)
	ListPublicQuestionLists(ctx context.Context) ([]QuestionListRecord, error)
	ListPrivateQuestionLists(ctx context.Context, ownerID string) ([]QuestionListRecord, error)
	CreateQuestion(ctx context.Context, q QuestionRecord) error
	ListQuestions(ctx context.Context, listID string) ([]QuestionRecord, error)
}
