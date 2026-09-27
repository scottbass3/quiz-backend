package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/scottbass3/quizz-backend/internal/domain"
	"github.com/scottbass3/quizz-backend/internal/game"
)

// GameStateStore implements game.StateStore on Redis.
//
// Keys of one game share the hash tag {gameID}, so they live in the same slot
// on Redis Cluster and can be used together in scripts:
//
//	game:{id}:meta           HASH  status, owner, config, progress fields
//	game:{id}:questions      HASH  question index -> JSON (with the answer)
//	game:{id}:players        HASH  player ID -> JSON
//	game:{id}:answers:<idx>  HASH  player ID -> option ID
//	game:{id}:lock           STRING lock token (SET NX PX)
//	actor:{actorID}:games    ZSET  game IDs scored by game creation time
//	games:deadlines          ZSET  "gameID|idx" scored by answer deadline (ms)
type GameStateStore struct {
	rdb         *redis.Client
	idleTTL     time.Duration // expiry of unfinished games, refreshed on every write
	finishedTTL time.Duration // expiry of finished games
	lockTTL     time.Duration // a crashed lock holder releases the lock after this
	lockWait    time.Duration // how long Lock waits before ErrBusy
}

var _ game.StateStore = (*GameStateStore)(nil)

func NewGameStateStore(rdb *redis.Client, idleTTL, finishedTTL time.Duration) *GameStateStore {
	return &GameStateStore{
		rdb:         rdb,
		idleTTL:     idleTTL,
		finishedTTL: finishedTTL,
		lockTTL:     10 * time.Second,
		lockWait:    5 * time.Second,
	}
}

const deadlinesKey = "games:deadlines"

func metaKey(id string) string                 { return "game:{" + id + "}:meta" }
func questionsKey(id string) string            { return "game:{" + id + "}:questions" }
func playersKey(id string) string              { return "game:{" + id + "}:players" }
func answersKey(id string, idx int) string     { return "game:{" + id + "}:answers:" + strconv.Itoa(idx) }
func lockKey(id string) string                 { return "game:{" + id + "}:lock" }
func actorGamesKey(actorID string) string      { return "actor:{" + actorID + "}:games" }
func deadlineMember(id string, idx int) string { return id + "|" + strconv.Itoa(idx) }

// storedPlayer and storedQuestion are the JSON encodings kept in Redis.
type storedPlayer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Lives   int    `json:"lives"`
	Active  bool   `json:"active"`
	ActorID string `json:"actor_id"`
}

type storedQuestion struct {
	ID              string                `json:"id"`
	QuestionListID  string                `json:"question_list_id"`
	Text            string                `json:"text"`
	Options         []domain.Option       `json:"options"`
	CorrectOptionID string                `json:"correct_option_id"`
	OrderIndex      int                   `json:"order_index"`
	Theme           *domain.QuestionTheme `json:"theme,omitempty"`
}

func ms(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func fromMS(s string) time.Time {
	n, _ := strconv.ParseInt(s, 10, 64)
	if n == 0 {
		return time.Time{}
	}
	return time.UnixMilli(n)
}

func boolField(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ── Create / load ────────────────────────────────────────────────────────────

func (st *GameStateStore) Create(ctx context.Context, s *game.State, questions []*domain.Question) error {
	qs := make(map[string]any, len(questions))
	for i, q := range questions {
		data, err := json.Marshal(storedQuestion{
			ID: q.ID, QuestionListID: q.QuestionListID, Text: q.Text, Options: q.Options,
			CorrectOptionID: q.CorrectOptionID, OrderIndex: q.OrderIndex, Theme: q.Theme,
		})
		if err != nil {
			return fmt.Errorf("redis: encode question: %w", err)
		}
		qs[strconv.Itoa(i)] = data
	}
	_, err := st.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, metaKey(s.ID), map[string]any{
			"owner_id":               s.OwnerID,
			"question_list_id":       s.QuestionListID,
			"created_at":             ms(s.CreatedAt),
			"initial_lives":          s.Config.InitialLives,
			"answer_timeout_seconds": s.Config.AnswerTimeoutSeconds,
			"total_questions":        s.TotalQuestions,
		})
		p.HSet(ctx, metaKey(s.ID), progressFields(s))
		if len(qs) > 0 {
			p.HSet(ctx, questionsKey(s.ID), qs)
		}
		st.expire(ctx, p, s.ID, st.idleTTL)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis: create game: %w", err)
	}
	return nil
}

// progressFields are the meta fields that change during a game.
func progressFields(s *game.State) map[string]any {
	return map[string]any{
		"status":         string(s.Status),
		"current_q_idx":  s.CurrentQIdx,
		"question_open":  boolField(s.QuestionOpen),
		"question_start": ms(s.QuestionStart),
		"end_reason":     string(s.EndReason),
	}
}

func (st *GameStateStore) Load(ctx context.Context, gameID string) (*game.State, error) {
	var metaCmd *redis.MapStringStringCmd
	var playersCmd *redis.MapStringStringCmd
	_, err := st.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		metaCmd = p.HGetAll(ctx, metaKey(gameID))
		playersCmd = p.HGetAll(ctx, playersKey(gameID))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("redis: load game: %w", err)
	}
	meta := metaCmd.Val()
	if len(meta) == 0 {
		return nil, game.ErrGameNotFound
	}

	s := &game.State{
		ID:             gameID,
		Status:         domain.GameStatus(meta["status"]),
		OwnerID:        meta["owner_id"],
		QuestionListID: meta["question_list_id"],
		CreatedAt:      fromMS(meta["created_at"]),
		TotalQuestions: atoi(meta["total_questions"]),
		CurrentQIdx:    atoi(meta["current_q_idx"]),
		QuestionOpen:   meta["question_open"] == "1",
		QuestionStart:  fromMS(meta["question_start"]),
		EndReason:      domain.GameOverReason(meta["end_reason"]),
		Config: game.EngineConfig{
			InitialLives:         atoi(meta["initial_lives"]),
			AnswerTimeoutSeconds: atoi(meta["answer_timeout_seconds"]),
		},
		Players: make(map[string]*domain.Player, len(playersCmd.Val())),
		Answers: map[string]string{},
	}
	for id, raw := range playersCmd.Val() {
		var sp storedPlayer
		if err := json.Unmarshal([]byte(raw), &sp); err != nil {
			return nil, fmt.Errorf("redis: decode player: %w", err)
		}
		s.Players[id] = &domain.Player{
			ID: sp.ID, Name: sp.Name, Lives: sp.Lives, Active: sp.Active, GameID: gameID, ActorID: sp.ActorID,
		}
	}

	if s.CurrentQIdx >= 0 {
		var qCmd *redis.StringCmd
		var answersCmd *redis.MapStringStringCmd
		_, err := st.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
			qCmd = p.HGet(ctx, questionsKey(gameID), strconv.Itoa(s.CurrentQIdx))
			answersCmd = p.HGetAll(ctx, answersKey(gameID, s.CurrentQIdx))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("redis: load current question: %w", err)
		}
		if s.Current, err = decodeQuestion(qCmd.Val()); err != nil {
			return nil, err
		}
		s.Answers = answersCmd.Val()
	}
	return s, nil
}

func (st *GameStateStore) Question(ctx context.Context, gameID string, idx int) (*domain.Question, error) {
	raw, err := st.rdb.HGet(ctx, questionsKey(gameID), strconv.Itoa(idx)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("redis: question %d of game %s: %w", idx, gameID, game.ErrGameNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("redis: load question: %w", err)
	}
	return decodeQuestion(raw)
}

func decodeQuestion(raw string) (*domain.Question, error) {
	var sq storedQuestion
	if err := json.Unmarshal([]byte(raw), &sq); err != nil {
		return nil, fmt.Errorf("redis: decode question: %w", err)
	}
	return &domain.Question{
		ID: sq.ID, QuestionListID: sq.QuestionListID, Text: sq.Text, Options: sq.Options,
		CorrectOptionID: sq.CorrectOptionID, OrderIndex: sq.OrderIndex, Theme: sq.Theme,
	}, nil
}

func (st *GameStateStore) Exists(ctx context.Context, gameID string) (bool, error) {
	n, err := st.rdb.Exists(ctx, metaKey(gameID)).Result()
	if err != nil {
		return false, fmt.Errorf("redis: game exists: %w", err)
	}
	return n == 1, nil
}

// ── Lock and writes ──────────────────────────────────────────────────────────

var unlockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)

func (st *GameStateStore) Lock(ctx context.Context, gameID string) (func(), error) {
	token := randomToken()
	key := lockKey(gameID)
	deadline := time.Now().Add(st.lockWait)
	delay := 2 * time.Millisecond
	for {
		ok, err := st.rdb.SetNX(ctx, key, token, st.lockTTL).Result()
		if err != nil {
			return nil, fmt.Errorf("redis: lock game: %w", err)
		}
		if ok {
			return func() {
				// Released with a fresh context: the caller's may be cancelled.
				rctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				unlockScript.Run(rctx, st.rdb, []string{key}, token)
			}, nil
		}
		if time.Now().After(deadline) {
			return nil, game.ErrBusy
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (st *GameStateStore) AddPlayer(ctx context.Context, gameID string, gameCreatedAt time.Time, p *domain.Player) error {
	data, err := encodePlayer(p)
	if err != nil {
		return err
	}
	_, err = st.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, playersKey(gameID), p.ID, data)
		pipe.ZAdd(ctx, actorGamesKey(p.ActorID), redis.Z{Score: float64(gameCreatedAt.UnixMilli()), Member: gameID})
		pipe.Expire(ctx, actorGamesKey(p.ActorID), st.idleTTL)
		st.expire(ctx, pipe, gameID, st.idleTTL)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis: add player: %w", err)
	}
	return nil
}

func (st *GameStateStore) Save(ctx context.Context, s *game.State, players ...*domain.Player) error {
	encoded := make(map[string]any, len(players))
	for _, p := range players {
		data, err := encodePlayer(p)
		if err != nil {
			return err
		}
		encoded[p.ID] = data
	}
	_, err := st.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, metaKey(s.ID), progressFields(s))
		if len(encoded) > 0 {
			p.HSet(ctx, playersKey(s.ID), encoded)
		}
		if s.Status == domain.GameStatusFinished {
			// Keep the final state readable for a while, answers included.
			st.expire(ctx, p, s.ID, st.finishedTTL)
			for i := 0; i <= s.CurrentQIdx; i++ {
				p.Expire(ctx, answersKey(s.ID, i), st.finishedTTL)
			}
		} else {
			st.expire(ctx, p, s.ID, st.idleTTL)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis: save game: %w", err)
	}
	return nil
}

func encodePlayer(p *domain.Player) ([]byte, error) {
	data, err := json.Marshal(storedPlayer{ID: p.ID, Name: p.Name, Lives: p.Lives, Active: p.Active, ActorID: p.ActorID})
	if err != nil {
		return nil, fmt.Errorf("redis: encode player: %w", err)
	}
	return data, nil
}

// expire sets ttl on the game's main keys.
func (st *GameStateStore) expire(ctx context.Context, p redis.Pipeliner, gameID string, ttl time.Duration) {
	p.Expire(ctx, metaKey(gameID), ttl)
	p.Expire(ctx, questionsKey(gameID), ttl)
	p.Expire(ctx, playersKey(gameID), ttl)
}

// ── Answers ──────────────────────────────────────────────────────────────────

// recordAnswerScript stores the first answer to the open question.
// KEYS: meta, answers. ARGV: question index, player ID, option ID, TTL (ms).
var recordAnswerScript = redis.NewScript(`
local m = redis.call('HMGET', KEYS[1], 'status', 'question_open', 'current_q_idx')
if not m[1] then return 'game_not_found' end
if m[1] ~= 'running' then return 'game_not_running' end
if m[2] ~= '1' or m[3] ~= ARGV[1] then return 'no_active_question' end
if redis.call('HSETNX', KEYS[2], ARGV[2], ARGV[3]) == 0 then return 'already_answered' end
redis.call('PEXPIRE', KEYS[2], ARGV[4])
return 'ok'`)

func (st *GameStateStore) RecordAnswer(ctx context.Context, gameID string, idx int, playerID, optionID string) error {
	res, err := recordAnswerScript.Run(ctx, st.rdb,
		[]string{metaKey(gameID), answersKey(gameID, idx)},
		idx, playerID, optionID, st.idleTTL.Milliseconds(),
	).Text()
	if err != nil {
		return fmt.Errorf("redis: record answer: %w", err)
	}
	switch res {
	case "ok":
		return nil
	case "game_not_found":
		return game.ErrGameNotFound
	case "game_not_running":
		return game.ErrGameNotRunning
	case "no_active_question":
		return game.ErrNoActiveQuestion
	case "already_answered":
		return game.ErrAlreadyAnswered
	default:
		return fmt.Errorf("redis: record answer: unexpected result %q", res)
	}
}

// closeAnswersScript marks the question closed and returns its answers.
// KEYS: meta, answers. ARGV: question index. Returns nil if that question
// is not the open one.
var closeAnswersScript = redis.NewScript(`
local m = redis.call('HMGET', KEYS[1], 'question_open', 'current_q_idx')
if m[1] ~= '1' or m[2] ~= ARGV[1] then return false end
redis.call('HSET', KEYS[1], 'question_open', '0')
return redis.call('HGETALL', KEYS[2])`)

func (st *GameStateStore) CloseAnswers(ctx context.Context, gameID string, idx int) (map[string]string, error) {
	res, err := closeAnswersScript.Run(ctx, st.rdb,
		[]string{metaKey(gameID), answersKey(gameID, idx)}, idx).StringSlice()
	if errors.Is(err, redis.Nil) {
		return nil, game.ErrNoActiveQuestion
	}
	if err != nil {
		return nil, fmt.Errorf("redis: close answers: %w", err)
	}
	answers := make(map[string]string, len(res)/2)
	for i := 0; i+1 < len(res); i += 2 {
		answers[res[i]] = res[i+1]
	}
	return answers, nil
}

// ── Answer deadlines ─────────────────────────────────────────────────────────

func (st *GameStateStore) ScheduleClose(ctx context.Context, gameID string, idx int, at time.Time) error {
	err := st.rdb.ZAdd(ctx, deadlinesKey, redis.Z{Score: float64(at.UnixMilli()), Member: deadlineMember(gameID, idx)}).Err()
	if err != nil {
		return fmt.Errorf("redis: schedule close: %w", err)
	}
	return nil
}

func (st *GameStateStore) CancelClose(ctx context.Context, gameID string, idx int) error {
	if err := st.rdb.ZRem(ctx, deadlinesKey, deadlineMember(gameID, idx)).Err(); err != nil {
		return fmt.Errorf("redis: cancel close: %w", err)
	}
	return nil
}

// claimDueScript removes and returns the deadlines reached, in one step, so
// each is handed to a single instance. KEYS: deadlines. ARGV: now (ms), max.
var claimDueScript = redis.NewScript(`
local due = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
if #due > 0 then redis.call('ZREM', KEYS[1], unpack(due)) end
return due`)

func (st *GameStateStore) ClaimDueCloses(ctx context.Context, now time.Time, max int) ([]game.DueClose, error) {
	members, err := claimDueScript.Run(ctx, st.rdb, []string{deadlinesKey}, now.UnixMilli(), max).StringSlice()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("redis: claim deadlines: %w", err)
	}
	due := make([]game.DueClose, 0, len(members))
	for _, m := range members {
		sep := strings.LastIndexByte(m, '|')
		if sep < 0 {
			continue
		}
		due = append(due, game.DueClose{GameID: m[:sep], Index: atoi(m[sep+1:])})
	}
	return due, nil
}

// ── Actor index ──────────────────────────────────────────────────────────────

func (st *GameStateStore) GamesOfActor(ctx context.Context, actorID string) ([]string, error) {
	key := actorGamesKey(actorID)
	ids, err := st.rdb.ZRevRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: games of actor: %w", err)
	}
	if len(ids) == 0 {
		return ids, nil
	}
	exists := make([]*redis.IntCmd, len(ids))
	if _, err := st.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, id := range ids {
			exists[i] = p.Exists(ctx, metaKey(id))
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("redis: games of actor: %w", err)
	}
	live := make([]string, 0, len(ids))
	var gone []any
	for i, id := range ids {
		if exists[i].Val() == 1 {
			live = append(live, id)
		} else {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 {
		st.rdb.ZRem(ctx, key, gone...) // best-effort cleanup of expired games
	}
	return live, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
