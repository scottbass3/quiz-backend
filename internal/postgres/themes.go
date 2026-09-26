package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/scottbass3/quizz-backend/internal/store"
)

// ── ThemeStore ───────────────────────────────────────────────────────────────

const themeColumns = `id, COALESCE(question_list_id, ''), name, description, created_at, updated_at`

func (db *DB) CreateTheme(ctx context.Context, t store.ThemeRecord) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO themes (id, question_list_id, name, description, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		t.ID, nullableText(t.QuestionListID), t.Name, t.Description, t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create theme: %w", mapError(err))
	}
	return nil
}

func (db *DB) GetTheme(ctx context.Context, id string) (*store.ThemeRecord, error) {
	row := db.pool.QueryRow(ctx, `SELECT `+themeColumns+` FROM themes WHERE id = $1`, id)
	t, err := scanTheme(row)
	if err != nil {
		return nil, fmt.Errorf("postgres: get theme: %w", mapError(err))
	}
	return t, nil
}

func (db *DB) ListGlobalThemes(ctx context.Context) ([]store.ThemeRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT `+themeColumns+` FROM themes WHERE question_list_id IS NULL ORDER BY lower(name)`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list global themes: %w", err)
	}
	defer rows.Close()
	return scanThemeRows(rows)
}

func (db *DB) ListQuestionListThemes(ctx context.Context, listID string) ([]store.ThemeRecord, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT `+themeColumns+` FROM themes WHERE question_list_id = $1 ORDER BY lower(name)`, listID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list question list themes: %w", err)
	}
	defer rows.Close()
	return scanThemeRows(rows)
}

func (db *DB) UpdateTheme(ctx context.Context, t store.ThemeRecord) error {
	tag, err := db.pool.Exec(ctx,
		`UPDATE themes SET name = $1, description = $2, updated_at = $3 WHERE id = $4`,
		t.Name, t.Description, t.UpdatedAt, t.ID,
	)
	if err != nil {
		return fmt.Errorf("postgres: update theme: %w", mapError(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: update theme: %w", store.ErrNotFound)
	}
	return nil
}

func (db *DB) DeleteTheme(ctx context.Context, id string) error {
	// question_list_questions.theme_id is ON DELETE SET NULL.
	tag, err := db.pool.Exec(ctx, `DELETE FROM themes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete theme: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("postgres: delete theme: %w", store.ErrNotFound)
	}
	return nil
}

func scanTheme(row pgx.Row) (*store.ThemeRecord, error) {
	t := &store.ThemeRecord{}
	if err := row.Scan(&t.ID, &t.QuestionListID, &t.Name, &t.Description, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Scope = themeScope(t.QuestionListID)
	return t, nil
}

func scanThemeRows(rows pgx.Rows) ([]store.ThemeRecord, error) {
	var themes []store.ThemeRecord
	for rows.Next() {
		t, err := scanTheme(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan theme: %w", err)
		}
		themes = append(themes, *t)
	}
	return themes, rows.Err()
}

func themeScope(questionListID string) store.ThemeScope {
	if questionListID == "" {
		return store.ThemeScopeGlobal
	}
	return store.ThemeScopeList
}

// mapError translates driver errors into store sentinel errors.
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return store.ErrConflict
	}
	return err
}
