package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/scottbass3/quizz-backend/internal/store"
)

// DB wraps a pgxpool and implements the store interfaces (themes in themes.go).
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

// migrationLockID identifies the advisory lock that serializes migrations.
const migrationLockID = 7_420_130_001

// RunMigrations executes the embedded SQL migration script.
// All statements are idempotent, so this is safe to call on every startup.
// Instances starting together take turns through an advisory lock: concurrent
// CREATE ... IF NOT EXISTS statements can otherwise fail on a fresh database.
func (db *DB) RunMigrations(ctx context.Context, sql string) error {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: run migrations: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("postgres: lock migrations: %w", err)
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, sql); err != nil {
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

func (db *DB) UpdateQuestionList(ctx context.Context, l store.QuestionListRecord) error {
	tag, err := db.pool.Exec(ctx,
		`UPDATE question_lists SET name = $1, description = $2, updated_at = $3 WHERE id = $4`,
		l.Name, l.Description, l.UpdatedAt, l.ID)
	if err != nil {
		return fmt.Errorf("postgres: update question list: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: update question list: %w", store.ErrNotFound)
	}
	return nil
}

func (db *DB) DeleteQuestionList(ctx context.Context, id string) error {
	// Questions and custom themes cascade; games.question_list_id is SET NULL.
	tag, err := db.pool.Exec(ctx, `DELETE FROM question_lists WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete question list: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: delete question list: %w", store.ErrNotFound)
	}
	return nil
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
		`INSERT INTO question_list_questions (id, question_list_id, text, options, correct_option_id, order_index, theme_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		q.ID, q.QuestionListID, q.Text, optionsJSON, q.CorrectOptionID, q.OrderIndex, nullableText(q.ThemeID),
	)
	if err != nil {
		return fmt.Errorf("postgres: create question: %w", err)
	}
	return nil
}

func (db *DB) UpdateQuestion(ctx context.Context, q store.QuestionRecord) error {
	optionsJSON, err := json.Marshal(q.Options)
	if err != nil {
		return fmt.Errorf("postgres: marshal options: %w", err)
	}
	tag, err := db.pool.Exec(ctx,
		`UPDATE question_list_questions
		 SET text = $1, options = $2, correct_option_id = $3, theme_id = $4
		 WHERE id = $5`,
		q.Text, optionsJSON, q.CorrectOptionID, nullableText(q.ThemeID), q.ID,
	)
	if err != nil {
		return fmt.Errorf("postgres: update question: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: update question: %w", store.ErrNotFound)
	}
	return nil
}

func (db *DB) DeleteQuestion(ctx context.Context, id string) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: delete question: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var listID string
	err = tx.QueryRow(ctx,
		`DELETE FROM question_list_questions WHERE id = $1 RETURNING question_list_id`, id).Scan(&listID)
	if err != nil {
		return fmt.Errorf("postgres: delete question: %w", mapError(err))
	}
	// Close the gap left in the ordering.
	if _, err := tx.Exec(ctx,
		`UPDATE question_list_questions q SET order_index = r.pos
		 FROM (SELECT id, row_number() OVER (ORDER BY order_index, created_at) - 1 AS pos
		       FROM question_list_questions WHERE question_list_id = $1) r
		 WHERE q.id = r.id`, listID); err != nil {
		return fmt.Errorf("postgres: renumber questions: %w", err)
	}
	return tx.Commit(ctx)
}

func (db *DB) ReorderQuestions(ctx context.Context, listID string, questionIDs []string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE question_list_questions q SET order_index = u.pos - 1
		 FROM unnest($2::text[]) WITH ORDINALITY AS u(id, pos)
		 WHERE q.id = u.id AND q.question_list_id = $1`,
		listID, questionIDs)
	if err != nil {
		return fmt.Errorf("postgres: reorder questions: %w", err)
	}
	return nil
}

// questionSelect reads questions with their optional theme.
const questionSelect = `
	SELECT q.id, q.question_list_id, q.text, q.options, q.correct_option_id, q.order_index,
	       COALESCE(t.id, ''), COALESCE(t.name, ''), COALESCE(t.question_list_id, '')
	FROM question_list_questions q
	LEFT JOIN themes t ON t.id = q.theme_id`

func (db *DB) GetQuestion(ctx context.Context, id string) (*store.QuestionRecord, error) {
	q, err := scanQuestion(db.pool.QueryRow(ctx, questionSelect+` WHERE q.id = $1`, id))
	if err != nil {
		return nil, fmt.Errorf("postgres: get question: %w", mapError(err))
	}
	return q, nil
}

func (db *DB) ListQuestions(ctx context.Context, listID string) ([]store.QuestionRecord, error) {
	rows, err := db.pool.Query(ctx,
		questionSelect+` WHERE q.question_list_id = $1 ORDER BY q.order_index ASC`, listID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list questions: %w", err)
	}
	defer rows.Close()

	var questions []store.QuestionRecord
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan question: %w", err)
		}
		questions = append(questions, *q)
	}
	return questions, rows.Err()
}

func scanQuestion(row pgx.Row) (*store.QuestionRecord, error) {
	var q store.QuestionRecord
	var optionsJSON []byte
	var themeName, themeListID string
	if err := row.Scan(&q.ID, &q.QuestionListID, &q.Text, &optionsJSON, &q.CorrectOptionID, &q.OrderIndex,
		&q.ThemeID, &themeName, &themeListID); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(optionsJSON, &q.Options); err != nil {
		return nil, fmt.Errorf("unmarshal options: %w", err)
	}
	if q.ThemeID != "" {
		q.Theme = &store.ThemeRef{ID: q.ThemeID, Name: themeName, Scope: themeScope(themeListID)}
	}
	return &q, nil
}

// nullableText returns nil for an empty string (maps to SQL NULL).
func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
