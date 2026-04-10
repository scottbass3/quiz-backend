package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/scottbass3/quizz-backend/internal/store"
)

// DB wraps a pgxpool and implements the store interfaces.
type DB struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse config: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (db *DB) Close() {
	db.pool.Close()
}

// RunMigrations executes the embedded SQL migration script.
// All statements use IF NOT EXISTS so this is safe to call on every startup.
func (db *DB) RunMigrations(ctx context.Context, sql string) error {
	if _, err := db.pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("postgres: run migrations: %w", err)
	}
	return nil
}

// ── GameStore ────────────────────────────────────────────────────────────────

func (db *DB) CreateGame(ctx context.Context, g store.GameRecord) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO games (id, owner_id, question_list_id, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		g.ID, g.OwnerID, nullableText(g.QuestionListID), g.Status, g.CreatedAt, g.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create game: %w", err)
	}
	return nil
}

func (db *DB) GetGame(ctx context.Context, id string) (*store.GameRecord, error) {
	row := db.pool.QueryRow(ctx,
		`SELECT id, owner_id, COALESCE(question_list_id, ''), status, created_at, updated_at
		 FROM games WHERE id = $1`, id)
	g := &store.GameRecord{}
	if err := row.Scan(&g.ID, &g.OwnerID, &g.QuestionListID, &g.Status, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, fmt.Errorf("postgres: get game: %w", err)
	}
	return g, nil
}

func (db *DB) UpdateGameStatus(ctx context.Context, id, status string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE games SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("postgres: update game status: %w", err)
	}
	return nil
}

// ── PlayerStore ──────────────────────────────────────────────────────────────

func (db *DB) CreatePlayer(ctx context.Context, p store.PlayerRecord) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO players (id, game_id, name, lives, active, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		p.ID, p.GameID, p.Name, p.Lives, p.Active, p.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create player: %w", err)
	}
	return nil
}

func (db *DB) GetPlayer(ctx context.Context, id string) (*store.PlayerRecord, error) {
	row := db.pool.QueryRow(ctx,
		`SELECT id, game_id, name, lives, active, created_at FROM players WHERE id = $1`, id)
	p := &store.PlayerRecord{}
	if err := row.Scan(&p.ID, &p.GameID, &p.Name, &p.Lives, &p.Active, &p.CreatedAt); err != nil {
		return nil, fmt.Errorf("postgres: get player: %w", err)
	}
	return p, nil
}

func (db *DB) ListPlayers(ctx context.Context, gameID string) ([]store.PlayerRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, game_id, name, lives, active, created_at FROM players WHERE game_id = $1`, gameID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list players: %w", err)
	}
	defer rows.Close()

	var players []store.PlayerRecord
	for rows.Next() {
		var p store.PlayerRecord
		if err := rows.Scan(&p.ID, &p.GameID, &p.Name, &p.Lives, &p.Active, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan player: %w", err)
		}
		players = append(players, p)
	}
	return players, rows.Err()
}

func (db *DB) UpdatePlayerLives(ctx context.Context, id string, lives int, active bool) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE players SET lives = $1, active = $2 WHERE id = $3`, lives, active, id)
	if err != nil {
		return fmt.Errorf("postgres: update player lives: %w", err)
	}
	return nil
}

// ── QuestionListStore ────────────────────────────────────────────────────────

func (db *DB) CreateQuestionList(ctx context.Context, l store.QuestionListRecord) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO question_lists (id, name, description, visibility, owner_type, owner_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		l.ID, l.Name, l.Description, l.Visibility, l.OwnerType, l.OwnerID, l.CreatedAt, l.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create question list: %w", err)
	}
	return nil
}

func (db *DB) GetQuestionList(ctx context.Context, id string) (*store.QuestionListRecord, error) {
	row := db.pool.QueryRow(ctx,
		`SELECT id, name, description, visibility, owner_type, owner_id, created_at, updated_at
		 FROM question_lists WHERE id = $1`, id)
	l := &store.QuestionListRecord{}
	if err := row.Scan(&l.ID, &l.Name, &l.Description, &l.Visibility, &l.OwnerType, &l.OwnerID, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, fmt.Errorf("postgres: get question list: %w", err)
	}
	return l, nil
}

func (db *DB) ListPublicQuestionLists(ctx context.Context) ([]store.QuestionListRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, name, description, visibility, owner_type, owner_id, created_at, updated_at
		 FROM question_lists WHERE visibility = 'public' ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list public question lists: %w", err)
	}
	defer rows.Close()
	return scanQuestionListRows(rows)
}

func (db *DB) ListPrivateQuestionLists(ctx context.Context, ownerID string) ([]store.QuestionListRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, name, description, visibility, owner_type, owner_id, created_at, updated_at
		 FROM question_lists WHERE visibility = 'private' AND owner_id = $1 ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list private question lists: %w", err)
	}
	defer rows.Close()
	return scanQuestionListRows(rows)
}

type rowScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanQuestionListRows(rows rowScanner) ([]store.QuestionListRecord, error) {
	var lists []store.QuestionListRecord
	for rows.Next() {
		var l store.QuestionListRecord
		if err := rows.Scan(&l.ID, &l.Name, &l.Description, &l.Visibility, &l.OwnerType, &l.OwnerID, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan question list: %w", err)
		}
		lists = append(lists, l)
	}
	return lists, rows.Err()
}

func (db *DB) CreateQuestion(ctx context.Context, q store.QuestionRecord) error {
	optionsJSON, err := json.Marshal(q.Options)
	if err != nil {
		return fmt.Errorf("postgres: marshal options: %w", err)
	}
	_, err = db.pool.Exec(ctx,
		`INSERT INTO question_list_questions (id, question_list_id, text, options, correct_option_id, order_index)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		q.ID, q.QuestionListID, q.Text, optionsJSON, q.CorrectOptionID, q.OrderIndex,
	)
	if err != nil {
		return fmt.Errorf("postgres: create question: %w", err)
	}
	return nil
}

func (db *DB) ListQuestions(ctx context.Context, listID string) ([]store.QuestionRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, question_list_id, text, options, correct_option_id, order_index
		 FROM question_list_questions
		 WHERE question_list_id = $1 ORDER BY order_index ASC`, listID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list questions: %w", err)
	}
	defer rows.Close()

	var questions []store.QuestionRecord
	for rows.Next() {
		var q store.QuestionRecord
		var optionsJSON []byte
		if err := rows.Scan(&q.ID, &q.QuestionListID, &q.Text, &optionsJSON, &q.CorrectOptionID, &q.OrderIndex); err != nil {
			return nil, fmt.Errorf("postgres: scan question: %w", err)
		}
		if err := json.Unmarshal(optionsJSON, &q.Options); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal options: %w", err)
		}
		questions = append(questions, q)
	}
	return questions, rows.Err()
}

// nullableText returns nil for an empty string (maps to SQL NULL).
func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
