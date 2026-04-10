package domain

import "time"

type GameStatus string

const (
	GameStatusWaiting  GameStatus = "waiting"
	GameStatusRunning  GameStatus = "running"
	GameStatusFinished GameStatus = "finished"
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

type Game struct {
	ID          string
	Status      GameStatus
	OwnerID     string
	Players     map[string]*Player
	Questions   []*Question
	CurrentQIdx int // -1 means no active question
	CreatedAt   time.Time
}

type Player struct {
	ID     string
	Name   string
	Lives  int
	Active bool
	GameID string
}

type Question struct {
	ID              string
	Text            string
	Options         []Option
	CorrectOptionID string
	Answers         map[string]*Answer // playerID -> answer
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
