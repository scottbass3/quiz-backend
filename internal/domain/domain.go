package domain

import "time"

type GameStatus string

const (
	GameStatusWaiting  GameStatus = "waiting"
	GameStatusRunning  GameStatus = "running"
	GameStatusFinished GameStatus = "finished"
)

// GameOverReason explains why a game finished.
type GameOverReason string

const (
	// GameOverLastPlayerStanding: a single active player is left, they win.
	GameOverLastPlayerStanding GameOverReason = "last_player_standing"
	// GameOverAllEliminated: every remaining player was eliminated on the same question.
	GameOverAllEliminated GameOverReason = "all_eliminated"
	// GameOverNoMoreQuestions: the last question was played with several survivors.
	// The survivor with the most lives wins; a tie is a draw.
	GameOverNoMoreQuestions GameOverReason = "no_more_questions"
)

// ListVisibility controls who can see a question list.
type ListVisibility string

const (
	ListVisibilityPublic  ListVisibility = "public"
	ListVisibilityPrivate ListVisibility = "private"
)

// ActorType identifies the kind of actor performing operations.
// Set from the OIDC role claim, or from the X-Debug-Actor-Type header in dev mode.
type ActorType string

const (
	ActorTypeAdmin ActorType = "admin"
	ActorTypeUser  ActorType = "user"
)

type EventType string

const (
	EventGameJoined       EventType = "game_joined"
	EventPlayerJoined     EventType = "player_joined"
	EventQuestionStarted  EventType = "question_started"
	EventAnswerSubmitted  EventType = "answer_submitted"
	EventAnswerRejected   EventType = "answer_rejected"
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
	QuestionListID string // references the list this game was created from
	Players        map[string]*Player
	Questions      []*Question    // runtime copy, loaded from the list at game creation
	CurrentQIdx    int            // index of the last started question, -1 before the first
	QuestionOpen   bool           // true between question_started and question_closed
	QuestionStart  time.Time      // when the current question was started
	EndReason      GameOverReason // empty until the game is finished
	CreatedAt      time.Time
}

type Player struct {
	ID      string
	Name    string
	Lives   int
	Active  bool
	GameID  string
	ActorID string // authenticated actor who created this player (auth.Actor.Sub)
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
	Theme           *QuestionTheme     // optional, nil when the question has no theme
	Answers         map[string]*Answer // runtime only
}

// QuestionTheme is the theme of a question as sent to players.
// Scope is "global" or "list".
type QuestionTheme struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Scope string `json:"scope"`
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
