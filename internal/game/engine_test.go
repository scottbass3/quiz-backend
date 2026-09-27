package game_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
	appredis "github.com/scottbass3/quizz-backend/internal/redis"
)

var ctx = context.Background()

const (
	idleTTL     = 2 * time.Hour
	finishedTTL = 10 * time.Minute
)

// stubHub captures broadcast calls for assertions.
type stubHub struct {
	mu     sync.Mutex
	events []domain.Event
	direct map[string][]domain.Event
}

func newStubHub() *stubHub {
	return &stubHub{direct: make(map[string][]domain.Event)}
}

func (s *stubHub) Broadcast(e domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *stubHub) BroadcastTo(playerID string, e domain.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.direct[playerID] = append(s.direct[playerID], e)
}

func (s *stubHub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
}

func (s *stubHub) broadcastTypes() []domain.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	types := make([]domain.EventType, len(s.events))
	for i, e := range s.events {
		types[i] = e.Type
	}
	return types
}

func (s *stubHub) count(typ domain.EventType) int {
	n := 0
	for _, t := range s.broadcastTypes() {
		if t == typ {
			n++
		}
	}
	return n
}

// lastPayload returns the payload of the last broadcast event of type typ.
func (s *stubHub) lastPayload(t *testing.T, typ domain.EventType) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].Type == typ {
			return s.events[i].Payload.(map[string]any)
		}
	}
	t.Fatalf("no %s event broadcast", typ)
	return nil
}

// cluster is a Redis server and the hubs of its games, shared by any number
// of managers ("instances").
type cluster struct {
	t   *testing.T
	mr  *miniredis.Miniredis
	rdb *goredis.Client

	mu   sync.Mutex
	hubs map[string]*stubHub
}

func newCluster(t *testing.T) *cluster {
	mr := miniredis.RunT(t)
	return &cluster{t: t, mr: mr, rdb: goredis.NewClient(&goredis.Options{Addr: mr.Addr()}), hubs: map[string]*stubHub{}}
}

func (c *cluster) hub(gameID string) *stubHub {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hubs[gameID] == nil {
		c.hubs[gameID] = newStubHub()
	}
	return c.hubs[gameID]
}

// instance returns a new manager over the shared Redis.
func (c *cluster) instance() *game.Manager {
	return game.NewManager(c.store(), func(id string) game.Broadcaster { return c.hub(id) },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func (c *cluster) store() *appredis.GameStateStore {
	return appredis.NewGameStateStore(c.rdb, idleTTL, finishedTTL)
}

func question(id string) *domain.Question {
	return &domain.Question{
		ID:   id,
		Text: "What is 2+2?",
		Options: []domain.Option{
			{ID: "a", Text: "3"},
			{ID: "b", Text: "4"},
			{ID: "c", Text: "5"},
		},
		CorrectOptionID: "b",
	}
}

func questions(n int) []*domain.Question {
	qs := make([]*domain.Question, n)
	for i := range qs {
		qs[i] = question(fmt.Sprintf("q%d", i+1))
	}
	return qs
}

// newGame creates game "g" (owner player "owner-1", not added) with the given questions.
func newGame(t *testing.T, m *game.Manager, cfg game.EngineConfig, qs []*domain.Question) *game.Engine {
	t.Helper()
	eng, err := m.Create(ctx, "g", "owner-1", "list-1", qs, cfg)
	if err != nil {
		t.Fatalf("create game: %v", err)
	}
	return eng
}

// twoPlayers creates game "g" with n questions and players p1 (Alice) and p2 (Bob).
func twoPlayers(t *testing.T, c *cluster, cfg game.EngineConfig, n int) *game.Engine {
	t.Helper()
	eng := newGame(t, c.instance(), cfg, questions(n))
	must(t, eng.AddPlayer(ctx, "p1", "Alice", "actor-1"))
	must(t, eng.AddPlayer(ctx, "p2", "Bob", "actor-2"))
	return eng
}

func state(t *testing.T, eng *game.Engine) *game.State {
	t.Helper()
	s, err := eng.State(ctx)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var defaultCfg = game.EngineConfig{InitialLives: 3}

// ── Players ──────────────────────────────────────────────────────────────────

func TestAddPlayer(t *testing.T) {
	c := newCluster(t)
	eng := newGame(t, c.instance(), defaultCfg, questions(1))

	must(t, eng.AddPlayer(ctx, "p1", "Alice", "actor-1"))
	if err := eng.AddPlayer(ctx, "p1", "Alice", "actor-1"); err != game.ErrPlayerAlreadyJoined {
		t.Fatalf("expected ErrPlayerAlreadyJoined, got %v", err)
	}
	p := c.hub("g").lastPayload(t, domain.EventPlayerJoined)
	if p["player_id"] != "p1" || p["name"] != "Alice" || p["lives"] != 3 {
		t.Fatalf("unexpected player_joined payload %v", p)
	}

	must(t, eng.StartNextQuestion(ctx))
	if err := eng.AddPlayer(ctx, "p2", "Bob", "actor-2"); err != game.ErrGameAlreadyStarted {
		t.Fatalf("join after start: expected ErrGameAlreadyStarted, got %v", err)
	}
}

func TestActorOwnership(t *testing.T) {
	c := newCluster(t)
	eng := newGame(t, c.instance(), defaultCfg, questions(1))

	if got := state(t, eng).HostActorID(); got != "" {
		t.Fatalf("expected no host before the owner joins, got %q", got)
	}
	must(t, eng.AddPlayer(ctx, "owner-1", "Host", "actor-host"))
	must(t, eng.AddPlayer(ctx, "p2", "Bob", "actor-bob"))

	s := state(t, eng)
	if got := s.HostActorID(); got != "actor-host" {
		t.Fatalf("expected host actor-host, got %q", got)
	}
	if got, ok := s.PlayerActorID("p2"); !ok || got != "actor-bob" {
		t.Fatalf("expected p2 owned by actor-bob, got %q (found=%v)", got, ok)
	}
	if _, ok := s.PlayerActorID("unknown"); ok {
		t.Fatal("unknown player must not be found")
	}
}

func TestActorView(t *testing.T) {
	c := newCluster(t)
	eng := newGame(t, c.instance(), defaultCfg, questions(1))
	must(t, eng.AddPlayer(ctx, "owner-1", "Host", "alice"))
	must(t, eng.AddPlayer(ctx, "p2", "Second device", "alice"))
	must(t, eng.AddPlayer(ctx, "p3", "Bob", "bob"))
	s := state(t, eng)

	if isHost, ids := s.ActorView("alice"); !isHost || len(ids) != 2 {
		t.Fatalf("alice: expected host with 2 players, got %v %v", isHost, ids)
	}
	if isHost, ids := s.ActorView("bob"); isHost || len(ids) != 1 || ids[0] != "p3" {
		t.Fatalf("bob: expected player p3, got %v %v", isHost, ids)
	}
	if isHost, ids := s.ActorView("carol"); isHost || len(ids) != 0 {
		t.Fatalf("carol: expected nothing, got %v %v", isHost, ids)
	}
}

// ── Questions ────────────────────────────────────────────────────────────────

func TestStartNextQuestion(t *testing.T) {
	c := newCluster(t)
	empty, err := c.instance().Create(ctx, "empty", "o", "", nil, defaultCfg)
	must(t, err)
	if err := empty.StartNextQuestion(ctx); err != game.ErrNoMoreQuestions {
		t.Fatalf("no questions: expected ErrNoMoreQuestions, got %v", err)
	}

	eng := twoPlayers(t, c, defaultCfg, 1)
	c.hub("g").reset()
	must(t, eng.StartNextQuestion(ctx))
	if types := c.hub("g").broadcastTypes(); len(types) != 1 || types[0] != domain.EventQuestionStarted {
		t.Fatalf("expected question_started event, got %v", types)
	}
	if s := state(t, eng); s.Status != domain.GameStatusRunning || !s.QuestionOpen || s.Current.ID != "q1" {
		t.Fatalf("unexpected state after start %+v", s)
	}
}

func TestStartWhileQuestionOpen(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 2)
	must(t, eng.StartNextQuestion(ctx))

	if err := eng.StartNextQuestion(ctx); err != game.ErrQuestionOpen {
		t.Fatalf("expected ErrQuestionOpen, got %v", err)
	}
	if idx := state(t, eng).CurrentQIdx; idx != 0 {
		t.Fatalf("question must not be skipped, current index %d", idx)
	}
}

func TestQuestionEventsAnnounceLastQuestion(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 2)
	hub := c.hub("g")

	must(t, eng.StartNextQuestion(ctx))
	if got := hub.lastPayload(t, domain.EventQuestionStarted)["is_last"]; got != false {
		t.Fatalf("first of two questions must not be last, got %v", got)
	}
	result, err := eng.CloseQuestion(ctx)
	must(t, err)
	if result.RemainingQuestions != 1 || result.GameOver {
		t.Fatalf("expected 1 remaining question and no game over, got %+v", result)
	}
	if got := hub.lastPayload(t, domain.EventQuestionClosed)["remaining_questions"]; got != 1 {
		t.Fatalf("expected remaining_questions=1 in question_closed, got %v", got)
	}

	must(t, eng.StartNextQuestion(ctx))
	if got := hub.lastPayload(t, domain.EventQuestionStarted)["is_last"]; got != true {
		t.Fatalf("second of two questions must be last, got %v", got)
	}
}

func TestQuestionStartedCarriesTheme(t *testing.T) {
	c := newCluster(t)
	themed := question("q1")
	themed.Theme = &domain.QuestionTheme{ID: "t1", Name: "Math", Scope: "global"}
	eng := newGame(t, c.instance(), defaultCfg, []*domain.Question{themed, question("q2")})
	must(t, eng.AddPlayer(ctx, "p1", "Alice", "a1"))
	must(t, eng.AddPlayer(ctx, "p2", "Bob", "a2"))
	hub := c.hub("g")

	must(t, eng.StartNextQuestion(ctx))
	if got, _ := hub.lastPayload(t, domain.EventQuestionStarted)["theme"].(*domain.QuestionTheme); got == nil || got.Name != "Math" {
		t.Fatalf("expected theme Math in question_started, got %v", got)
	}
	_, err := eng.CloseQuestion(ctx)
	must(t, err)
	must(t, eng.StartNextQuestion(ctx))
	if got, _ := hub.lastPayload(t, domain.EventQuestionStarted)["theme"].(*domain.QuestionTheme); got != nil {
		t.Fatalf("expected no theme, got %+v", got)
	}
}

func TestCurrentQuestion(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, game.EngineConfig{InitialLives: 3, AnswerTimeoutSeconds: 30}, 2)

	if state(t, eng).CurrentQuestion() != nil {
		t.Fatal("no question before the first start")
	}
	before := time.Now().Add(-time.Second) // stored times have millisecond precision
	must(t, eng.StartNextQuestion(ctx))
	must(t, eng.SubmitAnswer(ctx, "p2", "q1", "a"))

	cq := state(t, eng).CurrentQuestion()
	if cq == nil || cq["question_id"] != "q1" || cq["is_last"] != false {
		t.Fatalf("unexpected current question %v", cq)
	}
	if _, leaks := cq["correct_option_id"]; leaks {
		t.Fatal("current question must not reveal the answer")
	}
	if got := cq["answered_by"].([]string); len(got) != 1 || got[0] != "p2" {
		t.Fatalf("expected answered_by [p2], got %v", got)
	}
	closesAt := cq["closes_at"].(time.Time)
	if closesAt.Before(before.Add(30*time.Second)) || closesAt.After(time.Now().Add(31*time.Second)) {
		t.Fatalf("closes_at should be ~30s after start, got %v", closesAt)
	}

	_, err := eng.CloseQuestion(ctx)
	must(t, err)
	if state(t, eng).CurrentQuestion() != nil {
		t.Fatal("no current question once closed")
	}
}

// ── Answers ──────────────────────────────────────────────────────────────────

func TestSubmitAnswer_CorrectThenClose(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 2)
	must(t, eng.StartNextQuestion(ctx))

	must(t, eng.SubmitAnswer(ctx, "p1", "q1", "b"))
	if err := eng.SubmitAnswer(ctx, "p1", "q1", "b"); err != game.ErrAlreadyAnswered {
		t.Fatalf("expected ErrAlreadyAnswered, got %v", err)
	}

	result, err := eng.CloseQuestion(ctx)
	must(t, err)
	if len(result.LifeLost) != 1 || result.LifeLost[0] != "p2" {
		t.Fatalf("expected p2 to lose a life, got %v", result.LifeLost)
	}
	if result.GameOver {
		t.Fatal("game should not be over yet")
	}
	s := state(t, eng)
	if s.Players["p1"].Lives != 3 || s.Players["p2"].Lives != 2 {
		t.Fatalf("unexpected lives p1=%d p2=%d", s.Players["p1"].Lives, s.Players["p2"].Lives)
	}
}

func TestSubmitAnswer_InvalidOption(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 2)
	must(t, eng.StartNextQuestion(ctx))

	if err := eng.SubmitAnswer(ctx, "p1", "q1", "zzz"); err != game.ErrInvalidOption {
		t.Fatalf("expected ErrInvalidOption, got %v", err)
	}
	// The invalid answer did not count: the player can still answer.
	must(t, eng.SubmitAnswer(ctx, "p1", "q1", "b"))
}

func TestAnswerAfterClose_Rejected(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 2)
	must(t, eng.StartNextQuestion(ctx))
	_, err := eng.CloseQuestion(ctx)
	must(t, err)

	if err := eng.SubmitAnswer(ctx, "p1", "q1", "b"); err != game.ErrNoActiveQuestion {
		t.Fatalf("expected ErrNoActiveQuestion, got %v", err)
	}
}

func TestConcurrentSubmitAnswer(t *testing.T) {
	c := newCluster(t)
	eng := newGame(t, c.instance(), defaultCfg, questions(2))
	for i := 0; i < 50; i++ {
		must(t, eng.AddPlayer(ctx, fmt.Sprintf("p%02d", i), "player", "actor"))
	}
	must(t, eng.StartNextQuestion(ctx))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := eng.SubmitAnswer(ctx, fmt.Sprintf("p%02d", i), "q1", "b"); err != nil {
				t.Errorf("answer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if n := len(state(t, eng).Answers); n != 50 {
		t.Fatalf("expected 50 answers, got %d", n)
	}
}

// Answers racing with a close are either counted or rejected, never lost:
// a player whose correct answer was accepted must not lose a life.
func TestAnswersRacingClose(t *testing.T) {
	c := newCluster(t)
	eng := newGame(t, c.instance(), defaultCfg, questions(2))
	const n = 40
	for i := 0; i < n; i++ {
		must(t, eng.AddPlayer(ctx, fmt.Sprintf("p%02d", i), "player", "actor"))
	}
	must(t, eng.StartNextQuestion(ctx))

	accepted := make([]bool, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			accepted[i] = eng.SubmitAnswer(ctx, fmt.Sprintf("p%02d", i), "q1", "b") == nil
		}(i)
	}
	_, err := eng.CloseQuestion(ctx)
	must(t, err)
	wg.Wait()

	s := state(t, eng)
	for i := 0; i < n; i++ {
		lives := s.Players[fmt.Sprintf("p%02d", i)].Lives
		if accepted[i] && lives != 3 {
			t.Errorf("p%02d: answer accepted but lost a life", i)
		}
		if !accepted[i] && lives != 2 {
			t.Errorf("p%02d: answer rejected but kept its lives", i)
		}
	}
}

// ── Close and game over ──────────────────────────────────────────────────────

func TestCloseQuestionTwice_PenalizesOnce(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 2)
	must(t, eng.StartNextQuestion(ctx))

	_, err := eng.CloseQuestion(ctx)
	must(t, err)
	if _, err := eng.CloseQuestion(ctx); err != game.ErrNoActiveQuestion {
		t.Fatalf("second close: expected ErrNoActiveQuestion, got %v", err)
	}
	if lives := state(t, eng).Players["p1"].Lives; lives != 2 {
		t.Fatalf("expected 2 lives after one missed question, got %d", lives)
	}
}

func TestPlayerEliminated(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, game.EngineConfig{InitialLives: 1}, 2)
	must(t, eng.StartNextQuestion(ctx))
	must(t, eng.SubmitAnswer(ctx, "p1", "q1", "b"))

	result, err := eng.CloseQuestion(ctx)
	must(t, err)
	if len(result.Eliminated) != 1 || result.Eliminated[0] != "p2" {
		t.Fatalf("expected p2 eliminated, got %v", result.Eliminated)
	}
	if !result.GameOver || result.Winner != "p1" || result.Reason != domain.GameOverLastPlayerStanding {
		t.Fatalf("expected p1 winning by last_player_standing, got %+v", result)
	}
}

func TestGameEndsAfterLastQuestion_LeaderWins(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 1)
	hub := c.hub("g")
	must(t, eng.StartNextQuestion(ctx))
	must(t, eng.SubmitAnswer(ctx, "p1", "q1", "b"))
	must(t, eng.SubmitAnswer(ctx, "p2", "q1", "a"))

	result, err := eng.CloseQuestion(ctx)
	must(t, err)
	if !result.GameOver || result.Winner != "p1" || len(result.Survivors) != 2 {
		t.Fatalf("expected p1 winning with 2 survivors, got %+v", result)
	}
	if result.Reason != domain.GameOverNoMoreQuestions || result.RemainingQuestions != 0 {
		t.Fatalf("expected no_more_questions with 0 remaining, got %q / %d", result.Reason, result.RemainingQuestions)
	}
	s := state(t, eng)
	if s.Status != domain.GameStatusFinished || s.EndReason != domain.GameOverNoMoreQuestions {
		t.Fatalf("expected finished game with end reason, got %q / %q", s.Status, s.EndReason)
	}
	if err := eng.StartNextQuestion(ctx); err != game.ErrNoMoreQuestions {
		t.Fatalf("expected ErrNoMoreQuestions, got %v", err)
	}
	if got := hub.lastPayload(t, domain.EventGameOver)["reason"]; got != domain.GameOverNoMoreQuestions {
		t.Fatalf("expected game_over reason no_more_questions, got %v", got)
	}
}

func TestGameEndsAfterLastQuestion_TieIsDraw(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 1)
	must(t, eng.StartNextQuestion(ctx))
	must(t, eng.SubmitAnswer(ctx, "p1", "q1", "b"))
	must(t, eng.SubmitAnswer(ctx, "p2", "q1", "b"))

	result, err := eng.CloseQuestion(ctx)
	must(t, err)
	if !result.GameOver || result.Winner != "" || len(result.Survivors) != 2 {
		t.Fatalf("expected a draw with 2 survivors, got %+v", result)
	}
}

func TestGameOver_AllEliminated(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, game.EngineConfig{InitialLives: 1}, 2)
	must(t, eng.StartNextQuestion(ctx))

	result, err := eng.CloseQuestion(ctx)
	must(t, err)
	if !result.GameOver || result.Reason != domain.GameOverAllEliminated || result.Winner != "" || len(result.Survivors) != 0 {
		t.Fatalf("expected game over by all_eliminated, got %+v", result)
	}
	if result.RemainingQuestions != 1 {
		t.Fatalf("expected 1 unplayed question, got %d", result.RemainingQuestions)
	}
	if err := eng.StartNextQuestion(ctx); err != game.ErrGameFinished {
		t.Fatalf("expected ErrGameFinished, got %v", err)
	}
}

func TestQuestionClosedCarriesScoreboard(t *testing.T) {
	c := newCluster(t)
	eng := twoPlayers(t, c, defaultCfg, 2)
	must(t, eng.StartNextQuestion(ctx))
	must(t, eng.SubmitAnswer(ctx, "p1", "q1", "b"))

	result, err := eng.CloseQuestion(ctx)
	must(t, err)
	scores, _ := c.hub("g").lastPayload(t, domain.EventQuestionClosed)["players"].([]game.PlayerScore)
	want := []game.PlayerScore{
		{ID: "p1", Name: "Alice", Lives: 3, Active: true},
		{ID: "p2", Name: "Bob", Lives: 2, Active: true},
	}
	if len(scores) != 2 || scores[0] != want[0] || scores[1] != want[1] {
		t.Fatalf("unexpected scoreboard %+v", scores)
	}
	if len(result.Players) != 2 {
		t.Fatalf("close result should carry the scoreboard, got %+v", result.Players)
	}
}

// ── Answer deadlines ─────────────────────────────────────────────────────────

func TestDeadline_ClosesOpenQuestion(t *testing.T) {
	c := newCluster(t)
	m := c.instance()
	eng := twoPlayers(t, c, game.EngineConfig{InitialLives: 3, AnswerTimeoutSeconds: 20}, 2)
	must(t, eng.StartNextQuestion(ctx))
	must(t, eng.SubmitAnswer(ctx, "p1", "q1", "b"))

	if n := m.CloseDue(ctx, time.Now()); n != 0 {
		t.Fatalf("nothing is due yet, closed %d", n)
	}
	if n := m.CloseDue(ctx, time.Now().Add(21*time.Second)); n != 1 {
		t.Fatalf("expected the question to be closed at its deadline, closed %d", n)
	}
	s := state(t, eng)
	if s.QuestionOpen || s.Players["p1"].Lives != 3 || s.Players["p2"].Lives != 2 {
		t.Fatalf("unexpected state after deadline: open=%v p1=%d p2=%d", s.QuestionOpen, s.Players["p1"].Lives, s.Players["p2"].Lives)
	}
}

// A manual close before the deadline must not be followed by a second close.
func TestDeadline_AfterManualCloseDoesNothing(t *testing.T) {
	c := newCluster(t)
	m := c.instance()
	eng := twoPlayers(t, c, game.EngineConfig{InitialLives: 3, AnswerTimeoutSeconds: 20}, 2)
	must(t, eng.StartNextQuestion(ctx))
	_, err := eng.CloseQuestion(ctx)
	must(t, err)

	m.CloseDue(ctx, time.Now().Add(time.Minute))
	if lives := state(t, eng).Players["p1"].Lives; lives != 2 {
		t.Fatalf("expected 2 lives, got %d (question closed twice)", lives)
	}
	if n := c.hub("g").count(domain.EventQuestionClosed); n != 1 {
		t.Fatalf("expected exactly one question_closed, got %d", n)
	}
}

// A deadline registered for an earlier question never closes a later one.
func TestDeadline_StaleDoesNotCloseNextQuestion(t *testing.T) {
	c := newCluster(t)
	m := c.instance()
	eng := twoPlayers(t, c, defaultCfg, 2)
	must(t, eng.StartNextQuestion(ctx))
	_, err := eng.CloseQuestion(ctx)
	must(t, err)
	must(t, eng.StartNextQuestion(ctx))

	must(t, c.store().ScheduleClose(ctx, "g", 0, time.Now().Add(-time.Second)))
	m.CloseDue(ctx, time.Now())

	if s := state(t, eng); !s.QuestionOpen || s.CurrentQIdx != 1 {
		t.Fatalf("question 1 must still be open, got open=%v idx=%d", s.QuestionOpen, s.CurrentQIdx)
	}
}

// ── Several instances ────────────────────────────────────────────────────────

// Two instances sharing Redis serve the same game: whichever instance handles
// a request sees the state written by the other.
func TestTwoInstancesShareGameState(t *testing.T) {
	c := newCluster(t)
	a, b := c.instance(), c.instance()

	onA := newGame(t, a, defaultCfg, questions(2))
	must(t, onA.AddPlayer(ctx, "owner-1", "Host", "host"))

	onB, err := b.Get(ctx, "g")
	must(t, err)
	must(t, onB.AddPlayer(ctx, "p2", "Bob", "bob"))
	must(t, onA.StartNextQuestion(ctx))
	must(t, onB.SubmitAnswer(ctx, "p2", "q1", "b"))
	if err := onA.SubmitAnswer(ctx, "p2", "q1", "a"); err != game.ErrAlreadyAnswered {
		t.Fatalf("second answer through another instance: expected ErrAlreadyAnswered, got %v", err)
	}

	result, err := onB.CloseQuestion(ctx)
	must(t, err)
	if len(result.LifeLost) != 1 || result.LifeLost[0] != "owner-1" {
		t.Fatalf("expected only the host to lose a life, got %v", result.LifeLost)
	}
	if s := state(t, onA); s.Players["p2"].Lives != 3 || s.Players["owner-1"].Lives != 2 {
		t.Fatalf("instance A sees stale lives: %+v", s.Players)
	}
}

// With several instances polling, each deadline is closed exactly once.
func TestDeadline_ClaimedByOneInstance(t *testing.T) {
	c := newCluster(t)
	instances := []*game.Manager{c.instance(), c.instance(), c.instance()}
	eng := twoPlayers(t, c, game.EngineConfig{InitialLives: 3, AnswerTimeoutSeconds: 5}, 2)
	must(t, eng.StartNextQuestion(ctx))

	later := time.Now().Add(10 * time.Second)
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for _, m := range instances {
		wg.Add(1)
		go func(m *game.Manager) {
			defer wg.Done()
			n := m.CloseDue(ctx, later)
			mu.Lock()
			total += n
			mu.Unlock()
		}(m)
	}
	wg.Wait()

	if total != 1 || c.hub("g").count(domain.EventQuestionClosed) != 1 {
		t.Fatalf("expected one close in total, got %d (%d question_closed)", total, c.hub("g").count(domain.EventQuestionClosed))
	}
}

// ── Expiry and listing ───────────────────────────────────────────────────────

func TestGameExpiry(t *testing.T) {
	c := newCluster(t)
	m := c.instance()

	waiting := newGame(t, m, defaultCfg, questions(1))
	must(t, waiting.AddPlayer(ctx, "owner-1", "Host", "host"))

	finished, err := m.Create(ctx, "done", "o", "", questions(1), game.EngineConfig{InitialLives: 1})
	must(t, err)
	must(t, finished.AddPlayer(ctx, "o", "Host", "host"))
	must(t, finished.StartNextQuestion(ctx))
	_, err = finished.CloseQuestion(ctx)
	must(t, err)

	c.mr.FastForward(finishedTTL + time.Second)
	if _, err := m.Get(ctx, "done"); err != game.ErrGameNotFound {
		t.Fatalf("finished game should expire after finishedTTL, got %v", err)
	}
	if _, err := m.Get(ctx, "g"); err != nil {
		t.Fatalf("idle game should still exist, got %v", err)
	}

	c.mr.FastForward(idleTTL)
	if _, err := m.Get(ctx, "g"); err != game.ErrGameNotFound {
		t.Fatalf("idle game should expire after idleTTL, got %v", err)
	}
	if games, err := m.GamesOf(ctx, "host"); err != nil || len(games) != 0 {
		t.Fatalf("expired games must not be listed, got %v %v", games, err)
	}
}

func TestGamesOf(t *testing.T) {
	c := newCluster(t)
	eng := newGame(t, c.instance(), defaultCfg, questions(1))
	must(t, eng.AddPlayer(ctx, "owner-1", "Host", "alice"))
	must(t, eng.AddPlayer(ctx, "p2", "Bob", "bob"))

	for actor, want := range map[string]int{"alice": 1, "bob": 1, "carol": 0} {
		games, err := c.instance().GamesOf(ctx, actor)
		must(t, err)
		if len(games) != want {
			t.Errorf("%s: expected %d games, got %d", actor, want, len(games))
		}
	}
}
