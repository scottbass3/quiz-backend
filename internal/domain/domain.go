package domain

import "time"

type GameStatus string

const (
	GameStatusWaiting  GameStatus = "waiting"
	GameStatusRunning  GameStatus = "running"
	GameStatusFinished GameStatus = "finished"
)

// ListVisibility controls who can see a question list.
type ListVisibility string

const (
	ListVisibilityPublic  ListVisibility = "public"
	ListVisibilityPrivate ListVisibility = "private"
)

// ActorType identifies the kind of actor performing operations.
// Used in the temporary dev auth simulation (X-Debug-Actor-Type header).
type ActorType string

const (
	ActorTypeAdmin ActorType = "admin"
	ActorTypeUser  ActorType = "user"
)

type EventType string

const (
	EventGameJoined       EventType = "game_joined"
	EventQuestionStarted  EventType = "question_started"
	EventAnswerSubmitted  EventType = "answer_submitted"
	EventQuestionClosed   EventType = "question_closed"
	EventLifeLost         EventType = "life_lost"
	EventPlayerEliminated EventType = "player_eliminated"
	EventGameOver         EventType = "game_over"
)

// QuestionList is a named, reusable set of questions.
// Public lists are created by admins; private lists are owned by authenticated users.
type QuestionList struct {
	ID          string
	Name        string
	Description string
	Visibility  ListVisibility
	OwnerType   ActorType
	OwnerID     string // empty for admin-created public lists
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Game struct {
	ID             string
	Status         GameStatus
	OwnerID        string
	QuestionListID string     // references the list this game was created from
	Players        map[string]*Player
	Questions      []*Question // runtime copy, loaded from the list at game creation
	CurrentQIdx    int         // -1 means no active question
	CreatedAt      time.Time
}

type Player struct {
	ID     string
	Name   string
	Lives  int
	Active bool
	GameID string
}

// Question holds both catalog metadata (QuestionListID, OrderIndex) and
// runtime state (Answers). Answers are never persisted; they live only in memory.
type Question struct {
	ID              string
	QuestionListID  string // catalog reference
	Text            string
	Options         []Option
	CorrectOptionID string
	OrderIndex      int
	Answers         map[string]*Answer // runtime only
}

type Option struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type Answer struct {
	PlayerID    string
	QuestionID  string
	OptionID    string
	Correct     bool
	SubmittedAt time.Time
}

type Event struct {
	Type    EventType `json:"type"`
	Payload any       `json:"payload,omitempty"`
}
