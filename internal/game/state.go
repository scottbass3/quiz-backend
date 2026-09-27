package game

import (
	"sort"
	"time"

	"github.com/scottbass3/quizz-backend/internal/domain"
)

// State is a game as loaded from the StateStore: everything needed to apply
// the rules and to describe the game to clients. It is a value read at one
// point in time; mutations go through Engine, never through a State.
type State struct {
	ID             string
	Status         domain.GameStatus
	OwnerID        string // player ID of the host's own player
	QuestionListID string
	CreatedAt      time.Time
	Config         EngineConfig
	TotalQuestions int

	CurrentQIdx   int  // index of the last started question, -1 before the first
	QuestionOpen  bool // true between question_started and question_closed
	QuestionStart time.Time
	EndReason     domain.GameOverReason // empty until the game is finished

	Players map[string]*domain.Player

	// Current is the last started question (nil before the first), and
	// Answers the options chosen for it so far, by player ID.
	Current *domain.Question
	Answers map[string]string
}

// PlayerScore is a player's standing, as sent in question_closed.
type PlayerScore struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Lives  int    `json:"lives"`
	Active bool   `json:"active"`
}

// RemainingQuestions counts the questions not started yet.
func (s *State) RemainingQuestions() int {
	return s.TotalQuestions - s.CurrentQIdx - 1
}

// HostActorID returns the actor who owns the host's player, i.e. the actor
// allowed to start and close questions. Empty until the owner is added.
func (s *State) HostActorID() string {
	if owner, ok := s.Players[s.OwnerID]; ok {
		return owner.ActorID
	}
	return ""
}

// PlayerActorID returns the actor that owns playerID.
func (s *State) PlayerActorID(playerID string) (string, bool) {
	p, ok := s.Players[playerID]
	if !ok {
		return "", false
	}
	return p.ActorID, true
}

// ActorView tells what actorID is in this game: whether it is the host, and
// the IDs of the players it owns (sorted).
func (s *State) ActorView(actorID string) (isHost bool, playerIDs []string) {
	playerIDs = []string{}
	for id, p := range s.Players {
		if p.ActorID == actorID {
			playerIDs = append(playerIDs, id)
		}
	}
	sort.Strings(playerIDs)
	return s.HostActorID() == actorID && actorID != "", playerIDs
}

// Scoreboard lists every player's lives, sorted by name then ID.
func (s *State) Scoreboard() []PlayerScore {
	scores := make([]PlayerScore, 0, len(s.Players))
	for _, p := range s.Players {
		scores = append(scores, PlayerScore{ID: p.ID, Name: p.Name, Lives: p.Lives, Active: p.Active})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Name != scores[j].Name {
			return scores[i].Name < scores[j].Name
		}
		return scores[i].ID < scores[j].ID
	})
	return scores
}

// questionPayload describes the current question for players (never the
// correct answer). Used by question_started and by CurrentQuestion, so a
// client that reconnects gets exactly what it would have received live.
// The game must have a started question.
func (s *State) questionPayload() map[string]any {
	q := s.Current
	payload := map[string]any{
		"question_id": q.ID,
		"index":       s.CurrentQIdx,
		"total":       s.TotalQuestions,
		"is_last":     s.CurrentQIdx == s.TotalQuestions-1,
		"text":        q.Text,
		"options":     q.Options,
		"theme":       q.Theme, // null when the question has no theme
	}
	if t := s.Config.AnswerTimeoutSeconds; t > 0 {
		payload["answer_timeout_seconds"] = t
		payload["closes_at"] = s.QuestionStart.Add(time.Duration(t) * time.Second).UTC()
	}
	return payload
}

// CurrentQuestion returns the open question as sent in question_started,
// plus "answered_by" (IDs of the players who already answered). It returns
// nil when no question is open.
func (s *State) CurrentQuestion() map[string]any {
	if !s.QuestionOpen || s.Current == nil {
		return nil
	}
	payload := s.questionPayload()
	answered := make([]string, 0, len(s.Answers))
	for pid := range s.Answers {
		answered = append(answered, pid)
	}
	sort.Strings(answered)
	payload["answered_by"] = answered
	return payload
}

// activePlayers returns the IDs of the players still in the game, sorted.
func (s *State) activePlayers() []string {
	active := make([]string, 0, len(s.Players))
	for id, p := range s.Players {
		if p.Active {
			active = append(active, id)
		}
	}
	sort.Strings(active)
	return active
}

// leader returns the player with strictly the most lives among ids, or ""
// if ids is empty or the top is tied.
func (s *State) leader(ids []string) string {
	leader, best, tied := "", -1, false
	for _, id := range ids {
		switch lives := s.Players[id].Lives; {
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

func hasOption(q *domain.Question, optionID string) bool {
	for _, o := range q.Options {
		if o.ID == optionID {
			return true
		}
	}
	return false
}
