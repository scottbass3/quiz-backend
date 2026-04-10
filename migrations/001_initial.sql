-- Migration: 001_initial
-- Creates the base tables for games, players, and questions.

CREATE TABLE IF NOT EXISTS games (
    id         TEXT PRIMARY KEY,
    owner_id   TEXT        NOT NULL,
    status     TEXT        NOT NULL DEFAULT 'waiting',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS players (
    id         TEXT PRIMARY KEY,
    game_id    TEXT        NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    name       TEXT        NOT NULL,
    lives      INT         NOT NULL DEFAULT 3,
    active     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_players_game_id ON players(game_id);

CREATE TABLE IF NOT EXISTS questions (
    id               TEXT PRIMARY KEY,
    game_id          TEXT        NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    text             TEXT        NOT NULL,
    options          JSONB       NOT NULL,
    correct_option_id TEXT       NOT NULL,
    idx              INT         NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_questions_game_id ON questions(game_id);
