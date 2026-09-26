-- Migration: 003_themes
-- Optional themes for catalog questions.
--   * Global themes (question_list_id IS NULL) are managed by admins and usable by any list.
--   * Custom themes (question_list_id set) belong to one question list and are only
--     usable by questions of that list.
-- A question references at most one theme; deleting a theme unassigns it.

CREATE TABLE IF NOT EXISTS themes (
    id               TEXT PRIMARY KEY,
    question_list_id TEXT        REFERENCES question_lists(id) ON DELETE CASCADE,
    name             TEXT        NOT NULL,
    description      TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Names are unique (case-insensitive) among global themes, and within each list.
CREATE UNIQUE INDEX IF NOT EXISTS ux_themes_global_name ON themes (lower(name))
    WHERE question_list_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS ux_themes_list_name ON themes (question_list_id, lower(name))
    WHERE question_list_id IS NOT NULL;

-- Nullable: existing questions keep working without a theme.
ALTER TABLE question_list_questions
    ADD COLUMN IF NOT EXISTS theme_id TEXT REFERENCES themes(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_qlq_theme_id ON question_list_questions(theme_id);
