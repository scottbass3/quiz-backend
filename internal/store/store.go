package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotFound is returned when the requested record does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict is returned when a write violates a uniqueness constraint.
	ErrConflict = errors.New("store: conflict")
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

	// ThemeID is the optional theme (global, or custom to the same list); empty
	// means no theme. Theme is its short form, filled on reads (null if none).
	ThemeID string    `json:"-"`
	Theme   *ThemeRef `json:"theme"`
}

// ThemeScope tells global themes apart from question-list custom themes.
type ThemeScope string

const (
	ThemeScopeGlobal ThemeScope = "global" // managed by admins, usable by every list
	ThemeScopeList   ThemeScope = "list"   // belongs to one question list
)

// ThemeRecord is the persistence model for a theme.
// QuestionListID is empty for a global theme.
type ThemeRecord struct {
	ID             string     `json:"id"`
	Scope          ThemeScope `json:"scope"`
	QuestionListID string     `json:"question_list_id,omitempty"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ThemeRef is the short form of a theme embedded in questions.
type ThemeRef struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Scope ThemeScope `json:"scope"`
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
	// GetQuestion returns ErrNotFound if the question does not exist.
	GetQuestion(ctx context.Context, id string) (*QuestionRecord, error)
	// UpdateQuestion replaces text, options, correct option and theme.
	// Returns ErrNotFound if the question does not exist.
	UpdateQuestion(ctx context.Context, q QuestionRecord) error
	ListQuestions(ctx context.Context, listID string) ([]QuestionRecord, error)
}

// ThemeStore handles global and question-list themes.
// Create and Update return ErrConflict when the name is already used in the
// same scope; Get, Update and Delete return ErrNotFound for an unknown theme.
type ThemeStore interface {
	CreateTheme(ctx context.Context, t ThemeRecord) error
	GetTheme(ctx context.Context, id string) (*ThemeRecord, error)
	ListGlobalThemes(ctx context.Context) ([]ThemeRecord, error)
	ListQuestionListThemes(ctx context.Context, listID string) ([]ThemeRecord, error)
	// UpdateTheme changes name and description only; a theme never changes scope.
	UpdateTheme(ctx context.Context, t ThemeRecord) error
	// DeleteTheme removes the theme; questions using it are left without theme.
	DeleteTheme(ctx context.Context, id string) error
}
