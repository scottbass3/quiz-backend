-- Migration: 002_question_lists
-- Introduces question_lists and question_list_questions tables.
-- Games now reference a question list as their source of questions.

CREATE TABLE IF NOT EXISTS question_lists (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    visibility  TEXT NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'private')),
    owner_type  TEXT NOT NULL DEFAULT 'admin' CHECK (owner_type IN ('admin', 'user')),
    owner_id    TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_question_lists_visibility ON question_lists(visibility);
CREATE INDEX IF NOT EXISTS idx_question_lists_owner_id   ON question_lists(owner_id);

-- Catalog questions: belong to a question list, ordered by order_index.
CREATE TABLE IF NOT EXISTS question_list_questions (
    id                TEXT PRIMARY KEY,
    question_list_id  TEXT        NOT NULL REFERENCES question_lists(id) ON DELETE CASCADE,
    text              TEXT        NOT NULL,
    options           JSONB       NOT NULL,
    correct_option_id TEXT        NOT NULL,
    order_index       INT         NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_qlq_list_id ON question_list_questions(question_list_id);

-- Add question_list_id reference to games (nullable: existing games are unaffected).
ALTER TABLE games ADD COLUMN IF NOT EXISTS question_list_id TEXT REFERENCES question_lists(id);
